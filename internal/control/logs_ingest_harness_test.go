package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
	"github.com/rpop-project/rpop/internal/store"
)

// ingestHarness runs a controller with both its console API and its southbound listener, the way an operator
// and a node each see it, so POST /southbound/v1/logs can be exercised end to end over real mTLS. It cannot
// reuse internal/agent's test helpers: that package imports internal/control, so the reverse import is not
// possible from here.
type ingestHarness struct {
	t          *testing.T
	db         *sql.DB
	logDir     string
	control    *Control
	logs       *observer.ObservedLogs
	console    http.Handler
	cookie     *http.Cookie
	southbound *httptest.Server
}

func newIngestHarness(t *testing.T) *ingestHarness {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	h := &ingestHarness{t: t, db: db, logDir: t.TempDir()}
	h.start()
	t.Cleanup(func() {
		h.control.StopAll()
		_ = h.control.Close(context.Background())
		h.southbound.Close()
	})
	return h
}

// start (re)builds the controller from the harness's database and log directory. restart calls this again to
// simulate a controller process exiting and starting again: SQLite-durable state (nodes, their log_hwm, site
// configuration, the internal CA) survives across the call; in-memory state (the node registry, per-node
// upload locks) does not, exactly like a real restart.
func (h *ingestHarness) start() {
	h.t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	h.logs = logs
	control, err := NewWithLogDir(store.New(h.db), logger, h.logDir)
	if err != nil {
		h.t.Fatal(err)
	}
	control.SetEmbeddedNode(false)
	h.control = control
	h.console = control.Handler()

	tlsConfig, err := control.SouthboundTLSConfig(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(control.SouthboundHandler())
	server.TLS, server.EnableHTTP2 = tlsConfig, true
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	h.southbound = server

	if h.cookie == nil {
		response := h.callRaw(http.MethodPost, "/api/auth/setup", `{"password":"test-admin-password-2026"}`)
		if response.Code != http.StatusOK {
			h.t.Fatalf("admin setup: %d %s", response.Code, response.Body.String())
		}
		h.cookie = response.Result().Cookies()[0]
	}
}

// restart simulates the controller process exiting and starting again against the same database and log
// directory (see start's doc comment).
func (h *ingestHarness) restart() {
	h.t.Helper()
	h.southbound.Close()
	h.start()
}

func (h *ingestHarness) callRaw(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if h.cookie != nil {
		request.AddCookie(h.cookie)
	}
	response := httptest.NewRecorder()
	h.console.ServeHTTP(response, request)
	return response
}

func (h *ingestHarness) call(method, path, body string, want int) *httptest.ResponseRecorder {
	h.t.Helper()
	response := h.callRaw(method, path, body)
	if response.Code != want {
		h.t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

// createNode registers a node through the console API and returns its join token.
func (h *ingestHarness) createNode(id string) string {
	h.t.Helper()
	response := h.call(http.MethodPost, "/api/nodes", fmt.Sprintf(`{"id":%q,"name":"Node %s"}`, id, id), http.StatusCreated)
	var created struct {
		JoinToken string `json:"joinToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.JoinToken == "" {
		h.t.Fatalf("create node response %s: %v", response.Body.String(), err)
	}
	return created.JoinToken
}

// placeSite creates a site placed on nodeID with access logging on adapterID, so nodeAdapterSet allows uploads
// referencing that adapter from that node.
func (h *ingestHarness) placeSite(id, nodeID, adapterID string) {
	h.t.Helper()
	body := fmt.Sprintf(`{"id":%q,"name":%q,"config":{"nodes":[%q],"listenPort":8080,"upstreams":[{"url":"http://upstream.test"}],"accessLog":{"adapterId":%q}}}`, id, id, nodeID, adapterID)
	h.call(http.MethodPost, "/api/sites", body, http.StatusCreated)
}

// createAdapter registers a new access log adapter and returns its ID.
func (h *ingestHarness) createAdapter(name string, config accesslog.Config) string {
	h.t.Helper()
	raw, err := json.Marshal(adapterMutation{Name: name, Config: config})
	if err != nil {
		h.t.Fatal(err)
	}
	response := h.call(http.MethodPost, "/api/logging/adapters", string(raw), http.StatusCreated)
	var created struct {
		SavedAdapterID string `json:"savedAdapterId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.SavedAdapterID == "" {
		h.t.Fatalf("create adapter response %s: %v", response.Body.String(), err)
	}
	return created.SavedAdapterID
}

// register performs the same CSR/register exchange internal/agent's node client does (see internal/agent's
// identity.go register), reimplemented here since that package cannot be imported from this one.
func (h *ingestHarness) register(joinToken string) *pki.Identity {
	h.t.Helper()
	token, err := pki.ParseJoinToken(joinToken)
	if err != nil {
		h.t.Fatal(err)
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(token.NodeID)
	if err != nil {
		h.t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: pki.BootstrapClientConfig(token.CAFingerprint)}}
	defer client.CloseIdleConnections()
	requestBody, err := json.Marshal(southbound.RegisterRequest{Token: joinToken, CSRPEM: csrPEM})
	if err != nil {
		h.t.Fatal(err)
	}
	response, err := client.Post(h.southbound.URL+southbound.RegisterPath, "application/json", bytes.NewReader(requestBody))
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		h.t.Fatalf("register: %d %s", response.StatusCode, data)
	}
	var registerResponse southbound.RegisterResponse
	if err := json.Unmarshal(data, &registerResponse); err != nil {
		h.t.Fatal(err)
	}
	identity, err := pki.LoadIdentity(registerResponse.CertificatePEM, keyPEM, registerResponse.CAPEM)
	if err != nil {
		h.t.Fatal(err)
	}
	return identity
}

func nodeClient(identity *pki.Identity) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: identity.ControllerClientConfig()}}
}

// reissueToken asks the console API for a fresh single-use join token on an already-registered node, so a test
// can simulate re-registration (a lost/reinstalled node coming back) by calling register again with it.
func (h *ingestHarness) reissueToken(nodeID string) string {
	h.t.Helper()
	response := h.call(http.MethodPost, "/api/nodes/"+nodeID+"/token", "", http.StatusOK)
	var created joinTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.JoinToken == "" {
		h.t.Fatalf("reissue token response %s: %v", response.Body.String(), err)
	}
	return created.JoinToken
}

// renew performs the same CSR/renew exchange internal/agent's node client does, mirroring register above: it
// authenticates with identity's current certificate and swaps in the renewed one, keeping the same node ID and
// certificate generation (see southboundRenew).
func (h *ingestHarness) renew(identity *pki.Identity) {
	h.t.Helper()
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(identity.NodeID)
	if err != nil {
		h.t.Fatal(err)
	}
	client := nodeClient(identity)
	defer client.CloseIdleConnections()
	requestBody, err := json.Marshal(southbound.RenewRequest{CSRPEM: csrPEM})
	if err != nil {
		h.t.Fatal(err)
	}
	response, err := client.Post(h.southbound.URL+southbound.RenewPath, "application/json", bytes.NewReader(requestBody))
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		h.t.Fatalf("renew: %d %s", response.StatusCode, data)
	}
	var renewResponse southbound.RenewResponse
	if err := json.Unmarshal(data, &renewResponse); err != nil {
		h.t.Fatal(err)
	}
	if err := identity.SetCertificate(renewResponse.CertificatePEM, keyPEM); err != nil {
		h.t.Fatal(err)
	}
}

// uploadSegment gzips lines as NDJSON and POSTs them as one log segment (D23's southbound protocol).
func (h *ingestHarness) uploadSegment(client *http.Client, segment uint64, lines []string) *http.Response {
	h.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		h.t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, h.southbound.URL+southbound.LogsPath, bytes.NewReader(buf.Bytes()))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set(southbound.LogSegmentHeader, strconv.FormatUint(segment, 10))
	response, err := client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	return response
}

func decodeAck(t *testing.T, response *http.Response) southbound.LogAck {
	t.Helper()
	defer response.Body.Close()
	var ack southbound.LogAck
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		t.Fatal(err)
	}
	return ack
}

func accessEnvelope(t *testing.T, adapterID string, record accesslog.Record) string {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(southbound.LogEnvelope{Kind: southbound.LogKindAccess, AdapterID: adapterID, Record: raw})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func tunnelEnvelope(t *testing.T, event overlay.TunnelEvent) string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(southbound.LogEnvelope{Kind: southbound.LogKindTunnel, Record: raw})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

// randomGzipStream streams a gzip-compressed stream of totalRawBytes of pseudo-random (so, incompressible)
// bytes, without ever holding the whole thing in memory: it is used to exercise the real wire-size cap
// (southbound.MaxLogSegmentBytes via http.MaxBytesReader) without allocating tens of megabytes up front.
func randomGzipStream(totalRawBytes int) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		gz := gzip.NewWriter(writer)
		source := rand.New(rand.NewPCG(1, 2))
		buf := make([]byte, 64<<10)
		remaining := totalRawBytes
		for remaining > 0 {
			n := len(buf)
			if remaining < n {
				n = remaining
			}
			for i := 0; i < n; i += 8 {
				binary.LittleEndian.PutUint64(buf[i:], source.Uint64())
			}
			if _, err := gz.Write(buf[:n]); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			remaining -= n
		}
		if err := gz.Close(); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return reader
}
