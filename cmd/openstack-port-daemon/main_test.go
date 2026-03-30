package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	th "github.com/gophercloud/gophercloud/testhelper"
	thclient "github.com/gophercloud/gophercloud/testhelper/client"

	"openstack-port/internal/api"
)

// ---------------------------------------------------------------------------
// TestBuildAuthOptsAllowReauth
// ---------------------------------------------------------------------------

// TestBuildAuthOptsAllowReauth verifies that buildAuthOpts always sets
// AllowReauth=true so that gophercloud transparently re-authenticates when a
// Keystone token expires (~1 h), avoiding "Authentication failed" errors in
// long-running daemon instances.
func TestBuildAuthOptsAllowReauth(t *testing.T) {
	// Set the required auth variables to known values.
	t.Setenv("OS_AUTH_URL", "http://keystone.example.com/v3")
	t.Setenv("OS_USERNAME", "test-user")
	t.Setenv("OS_PASSWORD", "test-pass")
	t.Setenv("OS_PROJECT_NAME", "test-project")
	t.Setenv("OS_DOMAIN_NAME", "Default")
	t.Setenv("OS_IDENTITY_API_VERSION", "3")

	// Clear other OS_* variables that could change AuthOptionsFromEnv() behavior.
	t.Setenv("OS_TOKEN", "")
	t.Setenv("OS_TENANT_ID", "")
	t.Setenv("OS_TENANT_NAME", "")
	t.Setenv("OS_PROJECT_ID", "")
	t.Setenv("OS_DOMAIN_ID", "")
	t.Setenv("OS_USER_DOMAIN_ID", "")
	t.Setenv("OS_USER_DOMAIN_NAME", "")
	t.Setenv("OS_PROJECT_DOMAIN_ID", "")
	t.Setenv("OS_PROJECT_DOMAIN_NAME", "")
	t.Setenv("OS_APPLICATION_CREDENTIAL_ID", "")
	t.Setenv("OS_APPLICATION_CREDENTIAL_NAME", "")
	t.Setenv("OS_APPLICATION_CREDENTIAL_SECRET", "")
	t.Setenv("OS_AUTH_TYPE", "")

	opts, err := buildAuthOpts()
	if err != nil {
		t.Fatalf("buildAuthOpts() error = %v", err)
	}
	if !opts.AllowReauth {
		t.Error("AllowReauth = false; want true so expired tokens are renewed automatically")
	}
}

// ---------------------------------------------------------------------------
// TestPortName
// ---------------------------------------------------------------------------

func TestPortName(t *testing.T) {
	tests := []struct {
		name        string
		containerID string
		want        string
	}{
		{"long ID truncated", "abcdef1234567890abcdef", "k8s-pod-abcdef123456"},
		{"exactly 12 chars", "abcdef123456", "k8s-pod-abcdef123456"},
		{"short ID unchanged", "abc", "k8s-pod-abc"},
		{"empty string", "", "k8s-pod-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := portName(tt.containerID)
			if got != tt.want {
				t.Errorf("portName(%q) = %q, want %q", tt.containerID, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestWriteJSON
// ---------------------------------------------------------------------------

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]string{"hello": "world"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["hello"] != "world" {
		t.Errorf("body = %v, want {hello:world}", body)
	}
}

// ---------------------------------------------------------------------------
// TestWriteError
// ---------------------------------------------------------------------------

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "something went wrong")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	var body api.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "something went wrong" {
		t.Errorf("error = %q, want %q", body.Error, "something went wrong")
	}
}

// ---------------------------------------------------------------------------
// TestHealthEndpoint
// ---------------------------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	th.SetupHTTP()
	defer th.TeardownHTTP()

	handler := newHandler(thclient.ServiceClient())

	t.Run("Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["status"] != "ok" {
			t.Errorf("body = %v, want {status:ok}", body)
		}
	})

	t.Run("WrongMethod", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}

// ---------------------------------------------------------------------------
// TestAddEndpoint
// ---------------------------------------------------------------------------

func TestAddEndpoint(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		// Mock port create
		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("unexpected method %s on /ports", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"port": {
					"id": "port-uuid-1234",
					"name": "k8s-pod-abcdef123456",
					"mac_address": "fa:16:3e:aa:bb:cc",
					"network_id": "net-uuid",
					"fixed_ips": [{"subnet_id": "subnet-uuid", "ip_address": "10.0.0.5"}],
					"status": "ACTIVE"
				}
			}`))
		})

		// Mock subnet get
		th.Mux.HandleFunc("/subnets/subnet-uuid", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"subnet": {
					"id": "subnet-uuid",
					"cidr": "10.0.0.0/24",
					"gateway_ip": "10.0.0.1",
					"network_id": "net-uuid"
				}
			}`))
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid","subnet_id":"subnet-uuid"}`)
		req := httptest.NewRequest(http.MethodPost, "/add", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp api.AddResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.PortID != "port-uuid-1234" {
			t.Errorf("PortID = %q, want %q", resp.PortID, "port-uuid-1234")
		}
		if resp.MACAddress != "fa:16:3e:aa:bb:cc" {
			t.Errorf("MACAddress = %q, want %q", resp.MACAddress, "fa:16:3e:aa:bb:cc")
		}
		if resp.IPAddress != "10.0.0.5" {
			t.Errorf("IPAddress = %q, want %q", resp.IPAddress, "10.0.0.5")
		}
		if resp.PrefixLength != "24" {
			t.Errorf("PrefixLength = %q, want %q", resp.PrefixLength, "24")
		}
		if resp.GatewayIP != "10.0.0.1" {
			t.Errorf("GatewayIP = %q, want %q", resp.GatewayIP, "10.0.0.1")
		}
	})

	t.Run("MissingFields", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/add", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{not json}`)
		req := httptest.NewRequest(http.MethodPost, "/add", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("WrongMethod", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		req := httptest.NewRequest(http.MethodGet, "/add", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("PortCreateFails", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "boom"}`))
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid","subnet_id":"subnet-uuid"}`)
		req := httptest.NewRequest(http.MethodPost, "/add", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		var errResp api.ErrorResponse
		if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if errResp.Error == "" {
			t.Error("expected non-empty error message")
		}
	})

	t.Run("WithSecurityGroups", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("unexpected method %s on /ports", r.Method)
			}
			var reqBody struct {
				Port struct {
					SecurityGroups []string `json:"security_groups"`
				} `json:"port"`
			}
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				t.Errorf("failed to decode request body: %v", err)
			}
			if len(reqBody.Port.SecurityGroups) != 2 {
				t.Errorf("expected 2 security groups, got %d: %v", len(reqBody.Port.SecurityGroups), reqBody.Port.SecurityGroups)
			} else {
				if reqBody.Port.SecurityGroups[0] != "sg-id-1" {
					t.Errorf("security_groups[0] = %q, want %q", reqBody.Port.SecurityGroups[0], "sg-id-1")
				}
				if reqBody.Port.SecurityGroups[1] != "sg-id-2" {
					t.Errorf("security_groups[1] = %q, want %q", reqBody.Port.SecurityGroups[1], "sg-id-2")
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"port": {
					"id": "port-uuid-sg",
					"name": "k8s-pod-abcdef123456",
					"mac_address": "fa:16:3e:aa:bb:cc",
					"network_id": "net-uuid",
					"fixed_ips": [{"subnet_id": "subnet-uuid", "ip_address": "10.0.0.5"}],
					"status": "ACTIVE"
				}
			}`))
		})

		th.Mux.HandleFunc("/subnets/subnet-uuid", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"subnet": {
					"id": "subnet-uuid",
					"cidr": "10.0.0.0/24",
					"gateway_ip": "10.0.0.1",
					"network_id": "net-uuid"
				}
			}`))
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid","subnet_id":"subnet-uuid","security_group_ids":["sg-id-1","sg-id-2"]}`)
		req := httptest.NewRequest(http.MethodPost, "/add", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var resp api.AddResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.PortID != "port-uuid-sg" {
			t.Errorf("PortID = %q, want %q", resp.PortID, "port-uuid-sg")
		}
	})
}

// ---------------------------------------------------------------------------
// TestDelEndpoint
// ---------------------------------------------------------------------------

func TestDelEndpoint(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected method %s on /ports", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"ports": [
					{"id": "port-uuid-1234", "name": "k8s-pod-abcdef123456", "mac_address": "fa:16:3e:aa:bb:cc"}
				]
			}`))
		})

		th.Mux.HandleFunc("/ports/port-uuid-1234", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("unexpected method %s on port delete", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid"}`)
		req := httptest.NewRequest(http.MethodPost, "/del", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var resp api.DelResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.OK {
			t.Error("expected OK=true")
		}
	})

	t.Run("NoPortsFound", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ports": []}`))
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid"}`)
		req := httptest.NewRequest(http.MethodPost, "/del", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var resp api.DelResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.OK {
			t.Error("expected OK=true")
		}
	})

	t.Run("MissingFields", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/del", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("WrongMethod", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		req := httptest.NewRequest(http.MethodGet, "/del", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}

// ---------------------------------------------------------------------------
// TestCheckEndpoint
// ---------------------------------------------------------------------------

func TestCheckEndpoint(t *testing.T) {
	t.Run("Exists", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"ports": [
					{"id": "port-uuid-1234", "name": "k8s-pod-abcdef123456", "mac_address": "fa:16:3e:aa:bb:cc"}
				]
			}`))
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid"}`)
		req := httptest.NewRequest(http.MethodPost, "/check", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var resp api.CheckResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.Exists {
			t.Error("expected Exists=true")
		}
	})

	t.Run("NotExists", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		th.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ports": []}`))
		})

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{"container_id":"abcdef1234567890","network_id":"net-uuid"}`)
		req := httptest.NewRequest(http.MethodPost, "/check", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var resp api.CheckResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Exists {
			t.Error("expected Exists=false")
		}
	})

	t.Run("MissingFields", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/check", body)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("WrongMethod", func(t *testing.T) {
		th.SetupHTTP()
		defer th.TeardownHTTP()

		handler := newHandler(thclient.ServiceClient())
		req := httptest.NewRequest(http.MethodGet, "/check", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}
