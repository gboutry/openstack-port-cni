// Package api defines the shared request/response types for communication
// between the thin CNI plugin and the thick daemon over a Unix domain socket.
package api

const (
	// SocketPath is the default Unix domain socket path for the daemon.
	SocketPath = "/var/run/openstack-cni/cni.sock"
)

// AddRequest is sent by the thin CNI to create a Neutron port.
type AddRequest struct {
	ContainerID string `json:"container_id"`
	NetworkID   string `json:"network_id"`
	SubnetID    string `json:"subnet_id"`
}

// AddResponse returns the Neutron port details needed for OVS delegation.
type AddResponse struct {
	PortID       string `json:"port_id"`
	MACAddress   string `json:"mac_address"`
	IPAddress    string `json:"ip_address"`
	PrefixLength string `json:"prefix_length"`
	GatewayIP    string `json:"gateway_ip"`
}

// DelRequest is sent by the thin CNI to delete a Neutron port.
type DelRequest struct {
	ContainerID string `json:"container_id"`
	NetworkID   string `json:"network_id"`
}

// DelResponse acknowledges a delete operation.
type DelResponse struct {
	OK bool `json:"ok"`
}

// CheckRequest is sent by the thin CNI to verify a Neutron port exists.
type CheckRequest struct {
	ContainerID string `json:"container_id"`
	NetworkID   string `json:"network_id"`
}

// CheckResponse reports whether the Neutron port exists.
type CheckResponse struct {
	Exists bool `json:"exists"`
}

// ErrorResponse is returned when the daemon encounters an error.
type ErrorResponse struct {
	Error string `json:"error"`
}
