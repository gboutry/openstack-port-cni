package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/containernetworking/cni/pkg/invoke"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/version"
	ovs_types "github.com/k8snetworkplumbingwg/ovs-cni/pkg/types"

	"openstack-port/internal/api"
)

// PluginConf is the config for the openstack-port wrapper CNI plugin.
type PluginConf struct {
	ovs_types.NetConf
	NetworkID      string `json:"network_id"`
	SubnetID       string `json:"subnet_id"`
	DelegatePlugin string `json:"delegate_plugin"`
	SocketPath     string `json:"socket_path,omitempty"`
}

func (c *PluginConf) socketPath() string {
	if c.SocketPath != "" {
		return c.SocketPath
	}
	return api.SocketPath
}

// daemonRequest sends an HTTP request over a Unix domain socket to the daemon.
func daemonRequest(socketPath, method, path string, reqBody, respBody interface{}) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	resp, err := client.Do(func() *http.Request {
		req, _ := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		return req
	}())
	if err != nil {
		return fmt.Errorf("daemon request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %v", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp api.ErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("daemon error: %s", errResp.Error)
		}
		return fmt.Errorf("daemon returned status %d: %s", resp.StatusCode, string(body))
	}

	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("failed to decode response: %v", err)
		}
	}
	return nil
}

func cmdAdd(args *skel.CmdArgs) error {
	conf := &PluginConf{}
	if err := json.Unmarshal(args.StdinData, conf); err != nil {
		return fmt.Errorf("failed to parse network config: %v", err)
	}

	socketPath := conf.socketPath()

	var resp api.AddResponse
	err := daemonRequest(socketPath, http.MethodPost, "/add", api.AddRequest{
		ContainerID: args.ContainerID,
		NetworkID:   conf.NetworkID,
		SubnetID:    conf.SubnetID,
	}, &resp)
	if err != nil {
		return err
	}

	// Initialize Args and CNI structs if they're nil
	if conf.Args == nil {
		conf.Args = &struct {
			CNI *ovs_types.CNIArgs `json:"cni,omitempty"`
		}{
			CNI: &ovs_types.CNIArgs{},
		}
	} else if conf.Args.CNI == nil {
		conf.Args.CNI = &ovs_types.CNIArgs{}
	}

	conf.Args.CNI.OvnPort = resp.PortID
	conf.Args.CNI.MAC = resp.MACAddress

	// Marshal NetConf to a map so we can add IPAM config
	var confMap map[string]interface{}
	netConfBytes, err := json.Marshal(conf.NetConf)
	if err != nil {
		daemonRequest(socketPath, http.MethodPost, "/del", api.DelRequest{
			ContainerID: args.ContainerID,
			NetworkID:   conf.NetworkID,
		}, nil)
		return fmt.Errorf("failed to marshal NetConf: %v", err)
	}
	if err := json.Unmarshal(netConfBytes, &confMap); err != nil {
		daemonRequest(socketPath, http.MethodPost, "/del", api.DelRequest{
			ContainerID: args.ContainerID,
			NetworkID:   conf.NetworkID,
		}, nil)
		return fmt.Errorf("failed to unmarshal NetConf to map: %v", err)
	}

	// Add IPAM configuration for static plugin
	confMap["ipam"] = map[string]interface{}{
		"type": "static",
		"addresses": []map[string]interface{}{
			{
				"address": fmt.Sprintf("%s/%s", resp.IPAddress, resp.PrefixLength),
				"gateway": resp.GatewayIP,
			},
		},
	}

	// Marshal final config for delegation
	stdinData, err := json.Marshal(confMap)
	if err != nil {
		daemonRequest(socketPath, http.MethodPost, "/del", api.DelRequest{
			ContainerID: args.ContainerID,
			NetworkID:   conf.NetworkID,
		}, nil)
		return fmt.Errorf("failed to marshal final config: %v", err)
	}

	// Delegate to OVS CNI
	result, err := invoke.DelegateAdd(context.TODO(), conf.DelegatePlugin, stdinData, nil)
	if err != nil {
		// Clean up the Neutron port on failure
		daemonRequest(socketPath, http.MethodPost, "/del", api.DelRequest{
			ContainerID: args.ContainerID,
			NetworkID:   conf.NetworkID,
		}, nil)
		return fmt.Errorf("failed to delegate to %s: %v", conf.DelegatePlugin, err)
	}

	return result.Print()
}

func cmdDel(args *skel.CmdArgs) error {
	conf := &PluginConf{}
	if err := json.Unmarshal(args.StdinData, conf); err != nil {
		return nil // Ignore parse errors on delete per CNI spec
	}

	socketPath := conf.socketPath()

	// Delegate the DEL command to OVS CNI first
	netConf, err := json.Marshal(conf.NetConf)
	if err != nil {
		return nil // Ignore marshal errors on delete per CNI spec
	}
	if err := invoke.DelegateDel(context.TODO(), conf.DelegatePlugin, netConf, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: local OVS delegate delete failed: %v\n", err)
	}

	// Clean up the Neutron port via daemon
	daemonRequest(socketPath, http.MethodPost, "/del", api.DelRequest{
		ContainerID: args.ContainerID,
		NetworkID:   conf.NetworkID,
	}, nil)

	return nil
}

func cmdCheck(args *skel.CmdArgs) error {
	conf := &PluginConf{}
	if err := json.Unmarshal(args.StdinData, conf); err != nil {
		return fmt.Errorf("failed to parse network config: %v", err)
	}

	socketPath := conf.socketPath()

	var resp api.CheckResponse
	err := daemonRequest(socketPath, http.MethodPost, "/check", api.CheckRequest{
		ContainerID: args.ContainerID,
		NetworkID:   conf.NetworkID,
	}, &resp)
	if err != nil {
		return err
	}

	if !resp.Exists {
		return fmt.Errorf("neutron port not found")
	}

	// Marshal NetConf to a map for delegation
	var confMap map[string]interface{}
	netConfBytes, err := json.Marshal(conf.NetConf)
	if err != nil {
		return fmt.Errorf("failed to marshal NetConf: %v", err)
	}
	if err := json.Unmarshal(netConfBytes, &confMap); err != nil {
		return fmt.Errorf("failed to unmarshal NetConf to map: %v", err)
	}

	stdinData, err := json.Marshal(confMap)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	return invoke.DelegateCheck(context.TODO(), conf.DelegatePlugin, stdinData, nil)
}

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{Add: cmdAdd, Check: cmdCheck, Del: cmdDel}, version.All, "openstack-port")
}
