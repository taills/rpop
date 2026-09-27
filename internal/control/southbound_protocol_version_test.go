package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
)

// setOrOmitProtocolVersionHeader sets Rpop-Protocol-Version to value, or leaves it unset when value is "", so a
// test can exercise the "node predates D27" path (see checkProtocolVersion's doc comment) alongside real
// version numbers on the same request builders.
func setOrOmitProtocolVersionHeader(request *http.Request, value string) {
	if value != "" {
		request.Header.Set(southbound.ProtocolVersionHeader, value)
	}
}

// mustReadBody reads at most 4KiB, bounding it defensively: every real body it is used for here (a JSON error
// or ack) is tiny, and the cap keeps a test that got an unexpectedly-live stream (e.g. a bug that answered a
// rejected watch call with 200 after all) failing promptly instead of hanging forever on an unbounded read.
func mustReadBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// protocolVersionWindowCases is the D27 table every one of the five southbound endpoints below is checked
// against: an omitted header (a pre-D27 node) and the real southbound.MinSupportedProtocolVersion/ProtocolVersion
// (both 1 today) behave identically here — accepted — since classifyProtocolVersion's own tests
// (TestClassifyProtocolVersionCoversEveryWindowOutcome) already cover the "behind but in window" branch this
// window cannot reach.
var protocolVersionWindowCases = []struct {
	name        string
	headerValue string
	wantStatus  int
}{
	{name: "missing header is treated as the minimum supported version and accepted", headerValue: "", wantStatus: http.StatusOK},
	{name: "equal to the controller's version is accepted", headerValue: "1", wantStatus: http.StatusOK},
	{name: "above the controller's version is rejected", headerValue: "2", wantStatus: http.StatusUpgradeRequired},
	{name: "below the minimum supported version is rejected", headerValue: "0", wantStatus: http.StatusUpgradeRequired},
}

// TestSouthboundRegisterEnforcesProtocolVersionWindow covers register: the node identifies itself by join
// token rather than certificate, so checkProtocolVersion runs after the token is confirmed to match a real,
// already-provisioned node (see southboundRegister), not before.
func TestSouthboundRegisterEnforcesProtocolVersionWindow(t *testing.T) {
	for _, tc := range protocolVersionWindowCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIngestHarness(t)
			joinToken := h.createNode("register-node")
			token, err := pki.ParseJoinToken(joinToken)
			if err != nil {
				t.Fatal(err)
			}
			_, csrPEM, err := pki.NewKeyAndCSR(token.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: pki.BootstrapClientConfig(token.CAFingerprint)}}
			defer client.CloseIdleConnections()
			body, err := json.Marshal(southbound.RegisterRequest{Token: joinToken, CSRPEM: csrPEM})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.RegisterPath, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			setOrOmitProtocolVersionHeader(request, tc.headerValue)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertProtocolVersionOutcome(t, response, tc.wantStatus)
		})
	}
}

// TestSouthboundRenewEnforcesProtocolVersionWindow covers renew, which authenticates by client certificate
// like watch/status/logs.
func TestSouthboundRenewEnforcesProtocolVersionWindow(t *testing.T) {
	for _, tc := range protocolVersionWindowCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIngestHarness(t)
			identity := h.register(h.createNode("renew-node"))
			_, csrPEM, err := pki.NewKeyAndCSR(identity.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			client := nodeClient(identity)
			defer client.CloseIdleConnections()
			body, err := json.Marshal(southbound.RenewRequest{CSRPEM: csrPEM})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.RenewPath, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			setOrOmitProtocolVersionHeader(request, tc.headerValue)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertProtocolVersionOutcome(t, response, tc.wantStatus)
		})
	}
}

// TestSouthboundWatchEnforcesProtocolVersionWindow covers watch: a rejection must happen before the stream's
// 200 response is written at all (see southboundWatch), so the client sees the 426 the same as any other
// endpoint rather than an OK header followed by a stream that never sends anything.
func TestSouthboundWatchEnforcesProtocolVersionWindow(t *testing.T) {
	for _, tc := range protocolVersionWindowCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIngestHarness(t)
			identity := h.register(h.createNode("watch-node"))
			client := nodeClient(identity)
			defer client.CloseIdleConnections()
			// A bounded context, rather than none, protects against a hang if an accepted call's response ever
			// turns out to have a live, never-closing NDJSON body (see assertProtocolVersionOutcome's doc
			// comment) — deliberately generous, since a real watch stream idles for southbound.PingInterval
			// between frames.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.southbound.URL+southbound.WatchPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			setOrOmitProtocolVersionHeader(request, tc.headerValue)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertProtocolVersionOutcome(t, response, tc.wantStatus)
		})
	}
}

// TestSouthboundStatusEnforcesProtocolVersionWindow covers status, whose accepted outcome is 204 No Content
// rather than the 200 the other four endpoints answer with (see southboundStatus).
func TestSouthboundStatusEnforcesProtocolVersionWindow(t *testing.T) {
	for _, tc := range protocolVersionWindowCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIngestHarness(t)
			identity := h.register(h.createNode("status-node"))
			client := nodeClient(identity)
			defer client.CloseIdleConnections()
			body, err := json.Marshal(southbound.Status{Version: "test", Running: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.StatusPath, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			setOrOmitProtocolVersionHeader(request, tc.headerValue)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := tc.wantStatus
			if wantStatus == http.StatusOK {
				wantStatus = http.StatusNoContent
			}
			assertProtocolVersionOutcome(t, response, wantStatus)
		})
	}
}

// TestSouthboundLogsEnforcesProtocolVersionWindow covers logs: the version check runs before segment framing
// or body decompression (see southboundLogs), so a rejected upload never touches the node's log high-water mark.
func TestSouthboundLogsEnforcesProtocolVersionWindow(t *testing.T) {
	for _, tc := range protocolVersionWindowCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIngestHarness(t)
			identity := h.register(h.createNode("logs-node"))
			client := nodeClient(identity)
			defer client.CloseIdleConnections()
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.LogsPath, bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Encoding", "gzip")
			request.Header.Set(southbound.LogSegmentHeader, strconv.FormatUint(1, 10))
			setOrOmitProtocolVersionHeader(request, tc.headerValue)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertProtocolVersionOutcome(t, response, tc.wantStatus)
		})
	}
}

// assertProtocolVersionOutcome checks the two things every southbound endpoint must do regardless of outcome
// (D27): answer with wantStatus, and always echo the controller's own Rpop-Protocol-Version, so a client can
// discover a mismatch from this one response whether or not its own request was accepted. It only reads the
// body on a 426: an accepted watch call's body is a live, unbounded NDJSON stream that never closes on its
// own, so draining it here would hang forever instead of just closing the connection.
func assertProtocolVersionOutcome(t *testing.T, response *http.Response, wantStatus int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body := mustReadBody(t, response)
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, wantStatus, body)
	}
	if got := response.Header.Get(southbound.ProtocolVersionHeader); got != strconv.Itoa(southbound.ProtocolVersion) {
		t.Fatalf("response %s = %q, want %q", southbound.ProtocolVersionHeader, got, strconv.Itoa(southbound.ProtocolVersion))
	}
	if wantStatus == http.StatusUpgradeRequired {
		body := mustReadBody(t, response)
		var decoded apiError
		if err := json.Unmarshal([]byte(body), &decoded); err != nil || decoded.Error == "" {
			t.Fatalf("426 response body = %q, want a JSON {error: ...}", body)
		}
	}
}

// TestNodeViewReportsProtocolVersionAndStatus covers the console-facing half of D27: after a node's first
// authenticated southbound call, both the single-node GET and the list carry protocolVersion/protocolStatus,
// and the embedded node always reports the controller's own version as "current" without ever making a
// southbound call at all.
func TestNodeViewReportsProtocolVersionAndStatus(t *testing.T) {
	h := newIngestHarness(t)
	h.control.SetEmbeddedNode(true)
	identity := h.register(h.createNode("view-node"))
	client := nodeClient(identity)
	defer client.CloseIdleConnections()
	body, err := json.Marshal(southbound.Status{Version: "test", Running: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.StatusPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	setOrOmitProtocolVersionHeader(request, "1")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status call = %d", response.StatusCode)
	}

	var single struct {
		ProtocolVersion int    `json:"protocolVersion"`
		ProtocolStatus  string `json:"protocolStatus"`
	}
	singleResponse := h.call(http.MethodGet, "/api/nodes/view-node", "", http.StatusOK)
	if err := json.Unmarshal(singleResponse.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.ProtocolVersion != southbound.ProtocolVersion || single.ProtocolStatus != "current" {
		t.Fatalf("single node view = %#v", single)
	}

	var list []struct {
		ID              string `json:"id"`
		ProtocolVersion int    `json:"protocolVersion"`
		ProtocolStatus  string `json:"protocolStatus"`
	}
	listResponse := h.call(http.MethodGet, "/api/nodes", "", http.StatusOK)
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var sawViewNode, sawLocal bool
	for _, entry := range list {
		switch entry.ID {
		case "view-node":
			sawViewNode = true
			if entry.ProtocolVersion != southbound.ProtocolVersion || entry.ProtocolStatus != "current" {
				t.Fatalf("view-node in list = %#v", entry)
			}
		case "local":
			sawLocal = true
			if entry.ProtocolVersion != southbound.ProtocolVersion || entry.ProtocolStatus != "current" {
				t.Fatalf("embedded node in list = %#v", entry)
			}
		}
	}
	if !sawViewNode || !sawLocal {
		t.Fatalf("node list did not include both nodes: %#v", list)
	}
}
