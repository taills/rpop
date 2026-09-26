package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnvDefault(t *testing.T) {
	t.Setenv("RPOP_TEST_SET", "0.0.0.0:60000")
	t.Setenv("RPOP_TEST_EMPTY", "")
	tests := []struct{ key, want string }{
		{"RPOP_TEST_SET", "0.0.0.0:60000"},
		{"RPOP_TEST_EMPTY", "fallback"},
		{"RPOP_TEST_UNSET", "fallback"},
	}
	for _, tt := range tests {
		if got := envDefault(tt.key, "fallback"); got != tt.want {
			t.Errorf("envDefault(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestHealthCheckURL(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"0.0.0.0:60000", "http://127.0.0.1:60000/api/health"},
		{":8080", "http://127.0.0.1:8080/api/health"},
		{"[::]:8080", "http://[::1]:8080/api/health"},
		{"127.0.0.1:60000", "http://127.0.0.1:60000/api/health"},
		{"192.168.1.5:9000", "http://192.168.1.5:9000/api/health"},
		{"[fd00::5]:9000", "http://[fd00::5]:9000/api/health"},
	}
	for _, tt := range tests {
		got, err := healthCheckURL(tt.addr)
		if err != nil || got != tt.want {
			t.Errorf("healthCheckURL(%q) = %q, %v; want %q", tt.addr, got, err, tt.want)
		}
	}
	for _, addr := range []string{"", "8080", "localhost"} {
		if _, err := healthCheckURL(addr); err == nil {
			t.Errorf("healthCheckURL(%q) expected an error", addr)
		}
	}
}

func TestRunHealthCheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"healthy", http.StatusOK, false},
		{"unhealthy", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/health" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()
			err := runHealthCheck(strings.TrimPrefix(server.URL, "http://"))
			if (err != nil) != tt.wantErr {
				t.Fatalf("runHealthCheck error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	if err := runHealthCheck("127.0.0.1:1"); err == nil {
		t.Fatal("expected an error when nothing is listening")
	}
}
