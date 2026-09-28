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

func TestEnvDefaultInt64(t *testing.T) {
	t.Setenv("RPOP_TEST_INT_SET", "12345")
	t.Setenv("RPOP_TEST_INT_EMPTY", "")
	t.Setenv("RPOP_TEST_INT_BAD", "not-a-number")
	tests := []struct {
		key  string
		want int64
	}{
		{"RPOP_TEST_INT_SET", 12345},
		{"RPOP_TEST_INT_EMPTY", 99},
		{"RPOP_TEST_INT_UNSET", 99},
		{"RPOP_TEST_INT_BAD", 99},
	}
	for _, tt := range tests {
		if got := envDefaultInt64(tt.key, 99); got != tt.want {
			t.Errorf("envDefaultInt64(%q) = %d, want %d", tt.key, got, tt.want)
		}
	}
}

func TestEnvDefaultInt(t *testing.T) {
	t.Setenv("RPOP_TEST_INT_SET", "12345")
	t.Setenv("RPOP_TEST_INT_EMPTY", "")
	t.Setenv("RPOP_TEST_INT_BAD", "not-a-number")
	tests := []struct {
		key  string
		want int
	}{
		{"RPOP_TEST_INT_SET", 12345},
		{"RPOP_TEST_INT_EMPTY", 99},
		{"RPOP_TEST_INT_UNSET", 99},
		{"RPOP_TEST_INT_BAD", 99},
	}
	for _, tt := range tests {
		if got := envDefaultInt(tt.key, 99); got != tt.want {
			t.Errorf("envDefaultInt(%q) = %d, want %d", tt.key, got, tt.want)
		}
	}
}

func TestEnvDefaultBool(t *testing.T) {
	t.Setenv("RPOP_TEST_BOOL_TRUE", "false")
	t.Setenv("RPOP_TEST_BOOL_FALSE", "true")
	t.Setenv("RPOP_TEST_BOOL_EMPTY", "")
	t.Setenv("RPOP_TEST_BOOL_BAD", "not-a-bool")
	tests := []struct {
		key      string
		fallback bool
		want     bool
	}{
		{"RPOP_TEST_BOOL_TRUE", true, false},
		{"RPOP_TEST_BOOL_FALSE", false, true},
		{"RPOP_TEST_BOOL_EMPTY", true, true},
		{"RPOP_TEST_BOOL_UNSET", true, true},
		{"RPOP_TEST_BOOL_BAD", true, true},
	}
	for _, tt := range tests {
		if got := envDefaultBool(tt.key, tt.fallback); got != tt.want {
			t.Errorf("envDefaultBool(%q, %v) = %v, want %v", tt.key, tt.fallback, got, tt.want)
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
			err := runHealthCheck(strings.TrimPrefix(server.URL, "http://"), "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("runHealthCheck error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	if err := runHealthCheck("127.0.0.1:1", ""); err == nil {
		t.Fatal("expected an error when nothing is listening")
	}
}

// TestRunHealthCheckSendsHostHeader covers -console-hostnames: once the console is restricted to specific
// hostnames, /api/health only answers on one of them, so runHealthCheck must send it as the request's Host.
func TestRunHealthCheckSendsHostHeader(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		if r.Host != "console.test" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := runHealthCheck(strings.TrimPrefix(server.URL, "http://"), "console.test"); err != nil {
		t.Fatalf("runHealthCheck with a Host header: %v", err)
	}
	if gotHost != "console.test" {
		t.Fatalf("server observed Host %q, want %q", gotHost, "console.test")
	}
	if err := runHealthCheck(strings.TrimPrefix(server.URL, "http://"), ""); err == nil {
		t.Fatal("expected an error when the Host header is omitted and the console restricts to one hostname")
	}
}

func TestSouthboundAddress(t *testing.T) {
	tests := []struct{ mode, configured, want string }{
		{modeAllInOne, "", ""},
		{modeAllInOne, ":9443", ":9443"},
		{modeController, "", defaultSouthboundAddr},
		{modeController, "10.0.0.1:9443", "10.0.0.1:9443"},
		{modeNode, ":9443", ""},
	}
	for _, tt := range tests {
		got, err := southboundAddress(tt.mode, tt.configured)
		if err != nil || got != tt.want {
			t.Errorf("southboundAddress(%q, %q) = %q, %v; want %q", tt.mode, tt.configured, got, err, tt.want)
		}
	}
	if _, err := southboundAddress("edge", ""); err == nil {
		t.Error("unknown mode was accepted")
	}
}

func TestParseConsoleHostnames(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		want        []string
		wantInvalid bool
	}{
		{name: "empty is unrestricted", raw: "", want: nil},
		{name: "whitespace only is unrestricted", raw: "  ,  ,", want: nil},
		{name: "single hostname", raw: "console.test", want: []string{"console.test"}},
		{name: "trims and lowercases", raw: " Console.Test , Admin.Test ", want: []string{"console.test", "admin.test"}},
		{name: "drops empty entries between commas", raw: "console.test,,admin.test", want: []string{"console.test", "admin.test"}},
		{name: "invalid hostname is rejected", raw: "not a hostname", wantInvalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConsoleHostnames(tc.raw)
			if tc.wantInvalid {
				if err == nil {
					t.Fatalf("parseConsoleHostnames(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseConsoleHostnames(%q): %v", tc.raw, err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("parseConsoleHostnames(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
