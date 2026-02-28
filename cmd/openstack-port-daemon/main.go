package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"golang.org/x/sys/unix"

	"openstack-port/internal/api"
)

// portName returns the deterministic Neutron port name for a container.
func portName(containerID string) string {
	id := containerID
	if len(id) > 12 {
		id = id[:12]
	}
	return fmt.Sprintf("k8s-pod-%s", id)
}

// peerCredListener wraps a net.UnixListener and verifies that connecting
// peers are root (UID 0) using SO_PEERCRED.
type peerCredListener struct {
	*net.UnixListener
}

func (l *peerCredListener) Accept() (net.Conn, error) {
	conn, err := l.AcceptUnix()
	if err != nil {
		return nil, err
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to get raw conn: %w", err)
	}
	var ucred *unix.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		ucred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("raw control: %w", err)
	}
	if credErr != nil {
		conn.Close()
		return nil, fmt.Errorf("getsockopt peercred: %w", credErr)
	}
	if ucred.Uid != 0 {
		conn.Close()
		return nil, fmt.Errorf("rejected non-root peer uid=%d", ucred.Uid)
	}
	return conn, nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}

// newHandler creates the HTTP handler with all API routes.
func newHandler(neutronClient *gophercloud.ServiceClient) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req api.AddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if req.ContainerID == "" || req.NetworkID == "" || req.SubnetID == "" {
			writeError(w, http.StatusBadRequest, "container_id, network_id, and subnet_id are required")
			return
		}
		log.Printf("ADD container_id=%s network_id=%s subnet_id=%s", req.ContainerID, req.NetworkID, req.SubnetID)

		name := portName(req.ContainerID)
		createOpts := ports.CreateOpts{
			Name:      name,
			NetworkID: req.NetworkID,
			FixedIPs: []ports.IP{
				{SubnetID: req.SubnetID},
			},
		}
		port, err := ports.Create(neutronClient, createOpts).Extract()
		if err != nil {
			log.Printf("ERROR creating port: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create port: %v", err))
			return
		}

		// Get subnet details for CIDR and gateway
		subnet, err := subnets.Get(neutronClient, req.SubnetID).Extract()
		if err != nil {
			log.Printf("ERROR getting subnet, cleaning up port %s: %v", port.ID, err)
			ports.Delete(neutronClient, port.ID)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get subnet: %v", err))
			return
		}

		// Extract prefix length from CIDR
		prefixLength := ""
		if parts := strings.SplitN(subnet.CIDR, "/", 2); len(parts) == 2 {
			prefixLength = parts[1]
		}

		// Find the IP on the requested subnet
		ipAddress := ""
		for _, ip := range port.FixedIPs {
			if ip.SubnetID == req.SubnetID {
				ipAddress = ip.IPAddress
				break
			}
		}

		log.Printf("ADD success port_id=%s mac=%s ip=%s", port.ID, port.MACAddress, ipAddress)
		writeJSON(w, http.StatusOK, api.AddResponse{
			PortID:       port.ID,
			MACAddress:   port.MACAddress,
			IPAddress:    ipAddress,
			PrefixLength: prefixLength,
			GatewayIP:    subnet.GatewayIP,
		})
	})

	mux.HandleFunc("/del", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req api.DelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if req.ContainerID == "" || req.NetworkID == "" {
			writeError(w, http.StatusBadRequest, "container_id and network_id are required")
			return
		}
		log.Printf("DEL container_id=%s network_id=%s", req.ContainerID, req.NetworkID)

		name := portName(req.ContainerID)
		listOpts := ports.ListOpts{
			Name:      name,
			NetworkID: req.NetworkID,
		}
		allPages, err := ports.List(neutronClient, listOpts).AllPages()
		if err != nil {
			log.Printf("ERROR listing ports: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list ports: %v", err))
			return
		}
		allPorts, err := ports.ExtractPorts(allPages)
		if err != nil {
			log.Printf("ERROR extracting ports: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to extract ports: %v", err))
			return
		}

		for _, p := range allPorts {
			if err := ports.Delete(neutronClient, p.ID).ExtractErr(); err != nil {
				// Don't error if port is already gone (404)
				if _, ok := err.(gophercloud.ErrDefault404); !ok {
					log.Printf("ERROR deleting port %s: %v", p.ID, err)
					writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete port %s: %v", p.ID, err))
					return
				}
			}
			log.Printf("DEL deleted port_id=%s", p.ID)
		}

		writeJSON(w, http.StatusOK, api.DelResponse{OK: true})
	})

	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req api.CheckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if req.ContainerID == "" || req.NetworkID == "" {
			writeError(w, http.StatusBadRequest, "container_id and network_id are required")
			return
		}
		log.Printf("CHECK container_id=%s network_id=%s", req.ContainerID, req.NetworkID)

		name := portName(req.ContainerID)
		listOpts := ports.ListOpts{
			Name:      name,
			NetworkID: req.NetworkID,
		}
		allPages, err := ports.List(neutronClient, listOpts).AllPages()
		if err != nil {
			log.Printf("ERROR listing ports: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list ports: %v", err))
			return
		}
		allPorts, err := ports.ExtractPorts(allPages)
		if err != nil {
			log.Printf("ERROR extracting ports: %v", err)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to extract ports: %v", err))
			return
		}

		exists := len(allPorts) > 0
		log.Printf("CHECK result exists=%v", exists)
		writeJSON(w, http.StatusOK, api.CheckResponse{Exists: exists})
	})

	return mux
}

func main() {
	log.SetPrefix("[openstack-port-daemon] ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	// --- OpenStack authentication from environment ---
	log.Println("authenticating with OpenStack from OS_* environment variables")
	authOpts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		log.Fatalf("failed to read OS_* env vars: %v", err)
	}
	provider, err := openstack.AuthenticatedClient(authOpts)
	if err != nil {
		log.Fatalf("failed to authenticate with OpenStack: %v", err)
	}
	neutronClient, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		log.Fatalf("failed to create Neutron client: %v", err)
	}
	log.Println("OpenStack authentication successful, Neutron client ready")

	// --- Prepare Unix domain socket ---
	socketDir := filepath.Dir(api.SocketPath)
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		log.Fatalf("failed to create socket dir %s: %v", socketDir, err)
	}
	// Remove stale socket
	if err := os.Remove(api.SocketPath); err != nil && !os.IsNotExist(err) {
		log.Fatalf("failed to remove stale socket: %v", err)
	}

	unixListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: api.SocketPath, Net: "unix"})
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", api.SocketPath, err)
	}
	if err := os.Chmod(api.SocketPath, 0660); err != nil {
		log.Fatalf("failed to chmod socket: %v", err)
	}
	listener := &peerCredListener{UnixListener: unixListener}
	log.Printf("listening on %s", api.SocketPath)

	// --- Server with graceful shutdown ---
	srv := &http.Server{Handler: newHandler(neutronClient)}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down", sig)
		srv.Shutdown(context.Background())
	}()

	log.Println("daemon started, serving requests")
	if err := srv.Serve(listener); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}

	// Clean up socket
	os.Remove(api.SocketPath)
	log.Println("daemon stopped")
}
