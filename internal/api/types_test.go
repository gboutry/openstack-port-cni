package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSocketPath(t *testing.T) {
	const want = "/var/run/openstack-cni/cni.sock"
	if SocketPath != want {
		t.Errorf("SocketPath = %q, want %q", SocketPath, want)
	}
}

func TestAddRequestJSON(t *testing.T) {
	orig := AddRequest{
		ContainerID: "ctr-1",
		NetworkID:   "net-1",
		SubnetID:    "sub-1",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Verify snake_case keys
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}
	for _, key := range []string{"container_id", "network_id", "subnet_id"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("expected JSON key %q not found", key)
		}
	}

	var got AddRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestAddResponseJSON(t *testing.T) {
	orig := AddResponse{
		PortID:       "port-1",
		MACAddress:   "fa:16:3e:aa:bb:cc",
		IPAddress:    "10.0.0.5",
		PrefixLength: "24",
		GatewayIP:    "10.0.0.1",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got AddResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestDelRequestJSON(t *testing.T) {
	orig := DelRequest{ContainerID: "ctr-1", NetworkID: "net-1"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got DelRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestDelResponseJSON(t *testing.T) {
	tests := []struct {
		name string
		resp DelResponse
	}{
		{"OK true", DelResponse{OK: true}},
		{"OK false", DelResponse{OK: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got DelResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != tc.resp {
				t.Errorf("round-trip mismatch: got %+v, want %+v", got, tc.resp)
			}
		})
	}
}

func TestCheckRequestJSON(t *testing.T) {
	orig := CheckRequest{ContainerID: "ctr-1", NetworkID: "net-1"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got CheckRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestCheckResponseJSON(t *testing.T) {
	tests := []struct {
		name string
		resp CheckResponse
	}{
		{"Exists true", CheckResponse{Exists: true}},
		{"Exists false", CheckResponse{Exists: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got CheckResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != tc.resp {
				t.Errorf("round-trip mismatch: got %+v, want %+v", got, tc.resp)
			}
		})
	}
}

func TestErrorResponseJSON(t *testing.T) {
	orig := ErrorResponse{Error: "something went wrong"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ErrorResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestAddRequestEmptyFields(t *testing.T) {
	var orig AddRequest
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal zero value: %v", err)
	}
	var got AddRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal zero value: %v", err)
	}
	if got != orig {
		t.Errorf("zero-value round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestJSONFieldNames(t *testing.T) {
	tests := []struct {
		name     string
		jsonStr  string
		target   interface{}
		expected interface{}
	}{
		{
			name:    "AddRequest",
			jsonStr: `{"container_id":"c","network_id":"n","subnet_id":"s"}`,
			target:  &AddRequest{},
			expected: &AddRequest{
				ContainerID: "c",
				NetworkID:   "n",
				SubnetID:    "s",
			},
		},
		{
			name:    "AddResponse",
			jsonStr: `{"port_id":"p","mac_address":"m","ip_address":"i","prefix_length":"l","gateway_ip":"g"}`,
			target:  &AddResponse{},
			expected: &AddResponse{
				PortID:       "p",
				MACAddress:   "m",
				IPAddress:    "i",
				PrefixLength: "l",
				GatewayIP:    "g",
			},
		},
		{
			name:    "DelRequest",
			jsonStr: `{"container_id":"c","network_id":"n"}`,
			target:  &DelRequest{},
			expected: &DelRequest{
				ContainerID: "c",
				NetworkID:   "n",
			},
		},
		{
			name:     "DelResponse",
			jsonStr:  `{"ok":true}`,
			target:   &DelResponse{},
			expected: &DelResponse{OK: true},
		},
		{
			name:    "CheckRequest",
			jsonStr: `{"container_id":"c","network_id":"n"}`,
			target:  &CheckRequest{},
			expected: &CheckRequest{
				ContainerID: "c",
				NetworkID:   "n",
			},
		},
		{
			name:     "CheckResponse",
			jsonStr:  `{"exists":true}`,
			target:   &CheckResponse{},
			expected: &CheckResponse{Exists: true},
		},
		{
			name:     "ErrorResponse",
			jsonStr:  `{"error":"bad"}`,
			target:   &ErrorResponse{},
			expected: &ErrorResponse{Error: "bad"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tc.jsonStr), tc.target); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(tc.target, tc.expected) {
				t.Errorf("got %+v, want %+v", tc.target, tc.expected)
			}
		})
	}
}
