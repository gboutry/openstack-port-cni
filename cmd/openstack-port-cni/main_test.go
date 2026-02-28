package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"

	"openstack-port/internal/api"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupFakeDelegatePlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ovs")
	content := `#!/bin/sh
if [ "$CNI_COMMAND" = "DEL" ]; then exit 0; fi
if [ "$CNI_COMMAND" = "CHECK" ]; then exit 0; fi
echo '{"cniVersion":"0.4.0","interfaces":[{"name":"eth0"}],"ips":[{"address":"10.0.0.5/24","gateway":"10.0.0.1"}]}'
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func setupMockDaemon(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "test.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/add", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.AddResponse{
			PortID:       "port-123",
			MACAddress:   "fa:16:3e:aa:bb:cc",
			IPAddress:    "10.0.0.5",
			PrefixLength: "24",
			GatewayIP:    "10.0.0.1",
		})
	})
	mux.HandleFunc("/del", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DelResponse{OK: true})
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.CheckResponse{Exists: true})
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func setupMockDaemonCheckNotFound(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "test.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.CheckResponse{Exists: false})
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func makeStdinData(sock string) []byte {
	data, _ := json.Marshal(map[string]interface{}{
		"cniVersion":      "0.4.0",
		"type":            "openstack-port-cni",
		"network_id":      "net-uuid",
		"subnet_id":       "subnet-uuid",
		"delegate_plugin": "ovs",
		"socket_path":     sock,
		"bridge":          "br-int",
	})
	return data
}

// ---------------------------------------------------------------------------
// Unit tests (run with -short)
// ---------------------------------------------------------------------------

func TestSocketPathDefault(t *testing.T) {
	c := &PluginConf{}
	if got := c.socketPath(); got != api.SocketPath {
		t.Fatalf("expected %q, got %q", api.SocketPath, got)
	}
}

func TestSocketPathOverride(t *testing.T) {
	custom := "/tmp/custom.sock"
	c := &PluginConf{SocketPath: custom}
	if got := c.socketPath(); got != custom {
		t.Fatalf("expected %q, got %q", custom, got)
	}
}

func TestDaemonRequestSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "test.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/add" {
			t.Errorf("expected /add, got %s", r.URL.Path)
		}
		var req api.AddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.ContainerID != "ctr-1" {
			t.Errorf("expected container_id ctr-1, got %s", req.ContainerID)
		}
		_ = json.NewEncoder(w).Encode(api.AddResponse{
			PortID:       "port-abc",
			MACAddress:   "fa:16:3e:00:00:01",
			IPAddress:    "10.0.0.10",
			PrefixLength: "24",
			GatewayIP:    "10.0.0.1",
		})
	})}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	var resp api.AddResponse
	err = daemonRequest(sock, http.MethodPost, "/add", api.AddRequest{
		ContainerID: "ctr-1",
		NetworkID:   "net-1",
		SubnetID:    "sub-1",
	}, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.PortID != "port-abc" {
		t.Fatalf("expected port-abc, got %s", resp.PortID)
	}
	if resp.MACAddress != "fa:16:3e:00:00:01" {
		t.Fatalf("expected fa:16:3e:00:00:01, got %s", resp.MACAddress)
	}
}

func TestDaemonRequestErrorResponse(t *testing.T) {
	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "test.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(api.ErrorResponse{Error: "bad network_id"})
	})}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	var resp api.AddResponse
	err = daemonRequest(sock, http.MethodPost, "/add", api.AddRequest{}, &resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad network_id") {
		t.Fatalf("expected error to contain 'bad network_id', got: %v", err)
	}
}

func TestDaemonRequestNon200(t *testing.T) {
	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "test.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal server error")
	})}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	var resp api.AddResponse
	err = daemonRequest(sock, http.MethodPost, "/add", api.AddRequest{}, &resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected error to contain status code 500, got: %v", err)
	}
}

func TestDaemonRequestConnectionRefused(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nonexistent.sock")
	var resp api.AddResponse
	err := daemonRequest(sock, http.MethodPost, "/add", api.AddRequest{}, &resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "daemon request failed") {
		t.Fatalf("expected connection error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestIntegrationCmdAdd(t *testing.T) {
	sock := setupMockDaemon(t)
	cniPath := setupFakeDelegatePlugin(t)
	t.Setenv("CNI_PATH", cniPath)

	args := &skel.CmdArgs{
		ContainerID: "ctr-add-1",
		Netns:       "/proc/1/ns/net",
		IfName:      "eth0",
		StdinData:   makeStdinData(sock),
	}

	// Capture stdout since result.Print() writes there
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := cmdAdd(args)

	_ = w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("cmdAdd returned error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "0.4.0") {
		t.Fatalf("expected CNI result with cniVersion in stdout, got: %s", output)
	}
}

func TestIntegrationCmdDel(t *testing.T) {
	sock := setupMockDaemon(t)
	cniPath := setupFakeDelegatePlugin(t)
	t.Setenv("CNI_PATH", cniPath)

	args := &skel.CmdArgs{
		ContainerID: "ctr-del-1",
		Netns:       "/proc/1/ns/net",
		IfName:      "eth0",
		StdinData:   makeStdinData(sock),
	}

	if err := cmdDel(args); err != nil {
		t.Fatalf("cmdDel returned error: %v", err)
	}
}

func TestIntegrationCmdCheck(t *testing.T) {
	sock := setupMockDaemon(t)
	cniPath := setupFakeDelegatePlugin(t)
	t.Setenv("CNI_PATH", cniPath)

	args := &skel.CmdArgs{
		ContainerID: "ctr-check-1",
		Netns:       "/proc/1/ns/net",
		IfName:      "eth0",
		StdinData:   makeStdinData(sock),
	}

	if err := cmdCheck(args); err != nil {
		t.Fatalf("cmdCheck returned error: %v", err)
	}
}

func TestIntegrationCmdCheckNotFound(t *testing.T) {
	sock := setupMockDaemonCheckNotFound(t)
	cniPath := setupFakeDelegatePlugin(t)
	t.Setenv("CNI_PATH", cniPath)

	args := &skel.CmdArgs{
		ContainerID: "ctr-check-2",
		Netns:       "/proc/1/ns/net",
		IfName:      "eth0",
		StdinData:   makeStdinData(sock),
	}

	err := cmdCheck(args)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "neutron port not found") {
		t.Fatalf("expected 'neutron port not found', got: %v", err)
	}
}

func TestIntegrationCmdAddDaemonDown(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nonexistent.sock")
	cniPath := setupFakeDelegatePlugin(t)
	t.Setenv("CNI_PATH", cniPath)

	args := &skel.CmdArgs{
		ContainerID: "ctr-add-down",
		Netns:       "/proc/1/ns/net",
		IfName:      "eth0",
		StdinData:   makeStdinData(sock),
	}

	err := cmdAdd(args)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "daemon request failed") {
		t.Fatalf("expected connection error, got: %v", err)
	}
}
