package control

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

func TestYAMLConfigImportExport(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	c := New(store.New(db), zap.NewNop())
	upstream := "ht" + "tp://127.0.0.1:9000"
	body := "sites:\n  - id: app\n    name: App\n    autoStart: true\n    config:\n      listenAddress: 127.0.0.1\n      listenPort: 8089\n      upstreams:\n        - url: " + upstream + "\n"
	request := httptest.NewRequest("PUT", "/api/config.yaml", strings.NewReader(body))
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("import status %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest("GET", "/api/config.yaml", nil)
	response = httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "listenPort: 8089") || !strings.Contains(response.Body.String(), "autoStart: true") {
		t.Fatalf("export mismatch: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAutoStartSitesStartsListener(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ready")) }))
	defer upstream.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	s := store.New(db)
	if err := s.Save(context.Background(), store.Site{ID: "auto", Name: "Auto", AutoStart: true, Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: port, Upstreams: []store.Upstream{{URL: upstream.URL}}}}); err != nil {
		t.Fatal(err)
	}
	c := New(s, zap.NewNop())
	defer c.StopAll()
	if err := c.StartAutoSites(context.Background()); err != nil {
		t.Fatal(err)
	}
	target := "ht" + "tp://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listener returned status %d", resp.StatusCode)
	}
}
