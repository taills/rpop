package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/sharedport"
)

// sharedPortAPICall issues one HTTP request against handler (a real Control.Handler(), like every other request
// in this file) and fails the test if the response status is not want — the shared boilerplate every scenario
// below needs, since the whole point of these tests is that a bad configuration is rejected by the real API
// (POST/PUT /api/sites, POST /api/sites/{id}/start), not by calling validateSharedPortPlacement directly.
func sharedPortAPICall(t *testing.T, handler http.Handler, cookie *http.Cookie, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

// TestValidateSharedPortPlacementRejectsMissingHostnameOnConsolesAddress covers the first pre-save check
// docs/architecture/control-data-plane.md §5 ("共享端口(第三段)") asks for: an embedded-node site placed on the
// console's own -addr, with no hostname to tell them apart, is rejected at POST /api/sites — the same error
// internal/sharedport.Registry.PutSite would give for real once the site actually started, just surfaced at
// save time instead of start time.
func TestValidateSharedPortPlacementRejectsMissingHostnameOnConsolesAddress(t *testing.T) {
	control := newTestControl(t)
	control.SetBootstrapInfo("test", "controller", "", "127.0.0.1:8080", nil)
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)

	site := `{"id":"a","name":"a","config":{"listenAddress":"127.0.0.1","listenPort":8080,"upstreams":[{"url":"http://upstream.example"}]}}`
	response := sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites", site, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "hostname") {
		t.Fatalf("expected a hostname-required error, got %s", response.Body.String())
	}
}

// TestValidateSharedPortPlacementRejectsConflictingScope covers "端口相同但地址不同": a site whose address
// shares a port with the console's own -addr, but does not normalize to the exact same address (one wildcard,
// one specific — internal/sharedport.Classify's Conflicting case), can never actually bind alongside it at the
// OS level, so this is rejected at save time with the same Chinese guidance sharedport.ConflictError gives.
func TestValidateSharedPortPlacementRejectsConflictingScope(t *testing.T) {
	control := newTestControl(t)
	control.SetBootstrapInfo("test", "controller", "", "127.0.0.1:8080", nil)
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)

	site := `{"id":"a","name":"a","config":{"listenAddress":"","listenPort":8080,"hostnames":["a.test"],"upstreams":[{"url":"http://upstream.example"}]}}`
	response := sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites", site, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "端口相同但绑定范围不同") {
		t.Fatalf("expected a scope-conflict error, got %s", response.Body.String())
	}
}

// TestValidateSharedPortPlacementRejectsInternalHostname covers pki.IsInternalHostname's own doc comment, which
// names the control plane's own pre-validation as a caller: a site can never claim "controller.rpop" or a
// "*.nodes.rpop" name, on any address, shared or not (dataplane.Engine.buildRuntime already enforces this at
// start time; this is the same rule surfaced at save time).
func TestValidateSharedPortPlacementRejectsInternalHostname(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)

	site := `{"id":"a","name":"a","config":{"listenAddress":"127.0.0.1","listenPort":18080,"hostnames":["controller.rpop"],"upstreams":[{"url":"http://upstream.example"}]}}`
	response := sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites", site, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "内部保留名") {
		t.Fatalf("expected an internal-hostname error, got %s", response.Body.String())
	}
}

// TestValidateSharedPortPlacementRejectsConsoleHostnameOverlap covers -console-hostnames: a plaintext site
// sharing the console's address cannot claim a hostname the console itself is restricted to answer, since
// dispatchPlain always prefers a matching site over the console and that would silently starve the console of
// it (see sharedport.checkPlainOwnerOverlap, which PutSite calls for real; HostnamesOverlap exposes the same
// rule for this pre-check).
func TestValidateSharedPortPlacementRejectsConsoleHostnameOverlap(t *testing.T) {
	control := newTestControl(t)
	control.SetBootstrapInfo("test", "controller", "", "127.0.0.1:8080", []string{"console.test"})
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)

	site := `{"id":"a","name":"a","config":{"listenAddress":"127.0.0.1","listenPort":8080,"hostnames":["console.test"],"upstreams":[{"url":"http://upstream.example"}]}}`
	response := sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites", site, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "重叠") {
		t.Fatalf("expected a console-hostname-overlap error, got %s", response.Body.String())
	}
}

// TestValidateSharedPortPlacementRejectsMissingHostnameOnNodeRelayPort covers a remote node placement: a TLS
// site placed on a registered node, whose listen address normalizes to exactly that node's relay listen address
// (relayListenAddress(node.relayAddress), the same address internal/overlay's relay port registers as a TLS
// owner — see paths.go), needs a hostname to share it, exactly like the console case above. It deliberately
// does not also cover a scope mismatch (one wildcard, one specific) against a node's computed relay address:
// unlike the console's own -addr, a node can locally override its actual bind with -relay-listen, which the
// controller cannot see — see validateSharedPortForNodeLocked's sharedPortOwner.addrIsAuthoritative doc comment,
// and internal/agent's TestSiteSharesItsNodesOwnRelayPort/TestTLSSiteSharesItsNodesOwnRelayPort, which rely on
// exactly that override to legitimately share what would otherwise look like a scope conflict from here.
func TestValidateSharedPortPlacementRejectsMissingHostnameOnNodeRelayPort(t *testing.T) {
	handler := newTestControl(t).Handler()
	cookie := setupAdminForTest(t, handler)
	sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/nodes", `{"id":"edge-1","name":"Edge 1","relayAddress":"203.0.113.5:9000"}`, http.StatusCreated)

	site := `{"id":"a","name":"a","config":{"nodes":["edge-1"],"listenAddress":"","listenPort":9000,"tls":true,"upstreams":[{"url":"http://upstream.example"}]}}`
	response := sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites", site, http.StatusBadRequest)
	if !strings.Contains(response.Body.String(), "hostname") {
		t.Fatalf("expected a hostname-required error, got %s", response.Body.String())
	}
}

// TestValidateSharedPortPlacementAllowsLegitimateReuseToSaveAndStart is the positive case: a plaintext site
// given a hostname, placed on the embedded node, sharing the console's real (bound) address, saves and starts
// successfully through the real HTTP API, and actually serves traffic alongside the console once started —
// pre-validation must never reject a configuration internal/sharedport genuinely admits.
func TestValidateSharedPortPlacementAllowsLegitimateReuseToSaveAndStart(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	logDir := t.TempDir()
	control, err := NewWithLogDir(s, zap.NewNop(), logDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		control.StopAll()
		_ = control.CloseAccessLogs(context.Background())
	})

	registry := sharedport.NewRegistry()
	control.SetSharedPortRegistry(registry)
	port := freeLoopbackPort(t)
	address := "127.0.0.1:" + strconv.Itoa(port)
	console := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("console")) })
	if err := registry.PutPlaintextOwner(address, console, nil); err != nil {
		t.Fatalf("PutPlaintextOwner: %v", err)
	}
	t.Cleanup(func() { registry.RemovePlaintextOwner(address) })
	control.SetBootstrapInfo("test", "all-in-one", "", address, nil)

	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)
	upstream := textServer(t, "shared-site")
	site := `{"id":"a","name":"a","config":{"listenAddress":"127.0.0.1","listenPort":` + strconv.Itoa(port) + `,"hostnames":["shared.test"],"upstreams":[{"url":"` + upstream + `"}]}}`
	sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites", site, http.StatusCreated)
	sharedPortAPICall(t, handler, cookie, http.MethodPost, "/api/sites/a/start", "", http.StatusOK)

	if got := httpGetHost(t, address, "shared.test"); got != "shared-site" {
		t.Fatalf("shared.test routed to %q, want the site", got)
	}
	if got := httpGetHost(t, address, "console.test"); got != "console" {
		t.Fatalf("console.test routed to %q, want the console", got)
	}
}
