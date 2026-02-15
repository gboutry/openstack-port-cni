package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/containernetworking/cni/pkg/invoke"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/version"
	ovs_types "github.com/k8snetworkplumbingwg/ovs-cni/pkg/types"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
)

// PluginConf is the config for the openstack-port wrapper CNI plugin.
type PluginConf struct {
	ovs_types.NetConf
	NetworkID      string `json:"network_id"`
	SubnetID       string `json:"subnet_id"`
	DelegatePlugin string `json:"delegate_plugin"` // e.g., "ovs"
}

func loadEnvFromFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("could not open env file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue // Skip malformed lines
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		os.Setenv(key, value)
	}

	return scanner.Err()
}

// authenticate configures the OpenStack client using standard OS_* environment variables
func authenticate() (*gophercloud.ServiceClient, error) {
	// TODO: We should ideally load the environment from a better place
	loadEnvFromFile("/opt/cni/os_env")
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("could not get openstack auth from env: %v", err)
	}

	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return nil, fmt.Errorf("could not authenticate to openstack: %v", err)
	}

	client, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	return client, err
}

func cmdAdd(args *skel.CmdArgs) error {
	// 1. Parse the incoming JSON config
	conf := &PluginConf{}
	if err := json.Unmarshal(args.StdinData, conf); err != nil {
		return fmt.Errorf("failed to parse network config: %v", err)
	}

	// 2. Authenticate to OpenStack
	client, err := authenticate()
	if err != nil {
		return err
	}

	subneDetails, err := subnets.Get(client, conf.SubnetID).Extract()
	if err != nil {
		return fmt.Errorf("failed to get subnet details: %v", err)
	}

	// 3. Create a Neutron port to reserve an IP and get a MAC + port ID
	portName := fmt.Sprintf("k8s-pod-%s", args.ContainerID[:12])
	createOpts := ports.CreateOpts{
		Name:      portName,
		NetworkID: conf.NetworkID,
		FixedIPs:  []ports.IP{{SubnetID: conf.SubnetID}},
	}

	port, err := ports.Create(client, createOpts).Extract()
	if err != nil {
		return fmt.Errorf("failed to create neutron port: %v", err)
	}

	if len(port.FixedIPs) == 0 {
		// Clean up the port we just created
		ports.Delete(client, port.ID)
		return fmt.Errorf("openstack created the port but assigned no IP")
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

	conf.Args.CNI.OvnPort = port.ID
	conf.Args.CNI.MAC = port.MACAddress

	// Extract IP address from the port
	openstackIP := port.FixedIPs[0].IPAddress

	// Extract prefix length from CIDR (e.g., "10.0.0.0/24" -> "24")
	cidrParts := strings.Split(subneDetails.CIDR, "/")
	prefixLen := "24" // default
	if len(cidrParts) == 2 {
		prefixLen = cidrParts[1]
	}

	// Marshal NetConf to a map so we can add IPAM config
	var confMap map[string]interface{}
	netConfBytes, err := json.Marshal(conf.NetConf)
	if err != nil {
		ports.Delete(client, port.ID)
		return fmt.Errorf("failed to marshal NetConf: %v", err)
	}
	if err := json.Unmarshal(netConfBytes, &confMap); err != nil {
		ports.Delete(client, port.ID)
		return fmt.Errorf("failed to unmarshal NetConf to map: %v", err)
	}

	// Add IPAM configuration for static plugin
	confMap["ipam"] = map[string]interface{}{
		"type": "static",
		"addresses": []map[string]interface{}{
			{
				"address": fmt.Sprintf("%s/%s", openstackIP, prefixLen),
				"gateway": subneDetails.GatewayIP,
			},
		},
	}

	// Marshal final config for delegation
	stdinData, err := json.Marshal(confMap)
	if err != nil {
		ports.Delete(client, port.ID)
		return fmt.Errorf("failed to marshal final config: %v", err)
	}

	// 5. Delegate the heavy lifting to the actual OVS CNI.
	// This will look for the binary named conf.DelegatePlugin in /opt/cni/bin/
	result, err := invoke.DelegateAdd(context.TODO(), conf.DelegatePlugin, stdinData, nil)
	if err != nil {
		// Clean up the Neutron port on failure
		ports.Delete(client, port.ID)
		return fmt.Errorf("failed to delegate to %s: %v", conf.DelegatePlugin, err)
	}

	// 6. Pass the OVS CNI's exact JSON result back up to Multus/Kubernetes
	return result.Print()
}

func cmdDel(args *skel.CmdArgs) error {
	conf := &PluginConf{}
	if err := json.Unmarshal(args.StdinData, conf); err != nil {
		return nil // Ignore parse errors on delete per CNI spec
	}

	// 1. Delegate the DEL command to OVS CNI FIRST.
	// We want the local host interfaces and veth pairs cleaned up before
	// we destroy the upstream OpenStack port.
	// Marshal NetConf for delegation
	netConf, err := json.Marshal(conf.NetConf)

	if err != nil {
		return nil // Ignore marshal errors on delete per CNI spec
	}
	if err := invoke.DelegateDel(context.TODO(), conf.DelegatePlugin, netConf, nil); err != nil {
		// Log the error, but continue so we don't orphan the OpenStack port
		fmt.Fprintf(os.Stderr, "warning: local OVS delegate delete failed: %v\n", err)
	}

	// 2. Clean up the Neutron port
	client, err := authenticate()
	if err != nil {
		return err
	}

	// Find and delete the Neutron port we created for this container
	portName := fmt.Sprintf("k8s-pod-%s", args.ContainerID[:12])
	listOpts := ports.ListOpts{Name: portName, NetworkID: conf.NetworkID}
	allPages, err := ports.List(client, listOpts).AllPages()
	if err != nil {
		return err
	}

	foundPorts, err := ports.ExtractPorts(allPages)
	if err != nil || len(foundPorts) == 0 {
		return nil // Port already gone
	}

	for _, p := range foundPorts {
		ports.Delete(client, p.ID)
	}

	return nil
}

func cmdCheck(args *skel.CmdArgs) error {
	// Parse the incoming JSON config
	conf := &PluginConf{}
	if err := json.Unmarshal(args.StdinData, conf); err != nil {
		return fmt.Errorf("failed to parse network config: %v", err)
	}

	// Verify the OpenStack port still exists
	client, err := authenticate()
	if err != nil {
		return err
	}

	portName := fmt.Sprintf("k8s-pod-%s", args.ContainerID[:12])
	listOpts := ports.ListOpts{Name: portName, NetworkID: conf.NetworkID}
	allPages, err := ports.List(client, listOpts).AllPages()
	if err != nil {
		return fmt.Errorf("failed to list neutron ports: %v", err)
	}

	foundPorts, err := ports.ExtractPorts(allPages)
	if err != nil || len(foundPorts) == 0 {
		return fmt.Errorf("neutron port not found for container")
	}

	// Marshal NetConf to a map (same as cmdAdd to ensure consistency)
	var confMap map[string]interface{}
	netConfBytes, err := json.Marshal(conf.NetConf)
	if err != nil {
		return fmt.Errorf("failed to marshal NetConf: %v", err)
	}
	if err := json.Unmarshal(netConfBytes, &confMap); err != nil {
		return fmt.Errorf("failed to unmarshal NetConf to map: %v", err)
	}

	// Marshal config for delegation
	stdinData, err := json.Marshal(confMap)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	// Delegate the check to ovs-cni
	return invoke.DelegateCheck(context.TODO(), conf.DelegatePlugin, stdinData, nil)
}

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{Add: cmdAdd, Check: cmdCheck, Del: cmdDel}, version.All, "openstack-port")
}
