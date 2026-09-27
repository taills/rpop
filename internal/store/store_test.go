package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSiteAndSecretRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	s := New(db)
	want := Site{ID: "alpha", Name: "Alpha", AutoStart: true, Config: Config{ListenAddress: "127.0.0.1", ListenPort: 8443, Upstreams: []Upstream{{URL: "https://backend.example.test"}}}}
	if err := s.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.Config.ListenPort != want.Config.ListenPort || got.Config.Upstreams[0].URL != want.Config.Upstreams[0].URL || !got.AutoStart {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
	autoSites, err := s.AutoStartSites(context.Background())
	if err != nil || len(autoSites) != 1 || autoSites[0].ID != want.ID {
		t.Fatalf("auto-start query mismatch: sites=%#v err=%v", autoSites, err)
	}
	secret := []byte("test-secret-material")
	if err := s.SaveSecret(context.Background(), "alpha", "tls-key", secret); err != nil {
		t.Fatal(err)
	}
	gotSecret, err := s.Secret(context.Background(), "alpha", "tls-key")
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSecret) != string(secret) {
		t.Fatal("secret round-trip mismatch")
	}
	if err := s.Delete(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "alpha"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNodeRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	s := New(db)
	node := Node{ID: "edge-1", Name: "Edge 1", RelayAddress: "203.0.113.5:7443", TokenHash: "abc", TokenExpiresAt: "2030-01-01T00:00:00Z"}
	if err := s.SaveNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(context.Background(), "edge-1")
	if err != nil || got.RelayAddress != node.RelayAddress || got.TokenHash != "abc" || got.CreatedAt == "" {
		t.Fatalf("round trip = %#v, %v", got, err)
	}
	got.CertGeneration, got.TokenHash = 2, ""
	if err := s.SaveNode(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].CertGeneration != 2 || nodes[0].TokenHash != "" || nodes[0].CreatedAt != got.CreatedAt {
		t.Fatalf("list = %#v, %v", nodes, err)
	}
	if err := s.DeleteNode(context.Background(), "edge-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNode(context.Background(), "edge-1"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("deleted node lookup error = %v", err)
	}
}

// TestNodeLogHWMSurvivesUnrelatedSaves checks D24's invariant that only UpdateNodeLogHWM moves the high-water
// mark: an admin edit that goes through SaveNode (renaming a node, reissuing its token, ...) must not reset or
// otherwise touch it, and the mark must still be there after a process restart (a fresh Store over the same db).
func TestNodeLogHWMSurvivesUnrelatedSaves(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	s := New(db)
	ctx := context.Background()
	node := Node{ID: "edge-1", Name: "Edge 1"}
	if err := s.SaveNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetNode(ctx, "edge-1"); err != nil || got.LogHWM != 0 {
		t.Fatalf("initial LogHWM = %d, %v, want 0", got.LogHWM, err)
	}
	if err := s.UpdateNodeLogHWM(ctx, "edge-1", 7); err != nil {
		t.Fatal(err)
	}

	// An unrelated edit built from a stale Node (as if read before the HWM update) must not roll it back.
	stale := node
	stale.Name = "Edge One"
	if err := s.SaveNode(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(ctx, "edge-1")
	if err != nil || got.Name != "Edge One" || got.LogHWM != 7 {
		t.Fatalf("after unrelated save: %#v, %v, want name=%q logHWM=7", got, err, "Edge One")
	}

	// Reopening the store (simulating a controller restart) must still see the persisted mark.
	restarted := New(db)
	if got, err := restarted.GetNode(ctx, "edge-1"); err != nil || got.LogHWM != 7 {
		t.Fatalf("LogHWM after restart = %d, %v, want 7", got.LogHWM, err)
	}

	if err := s.UpdateNodeLogHWM(ctx, "missing", 1); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("UpdateNodeLogHWM on a missing node = %v, want ErrNodeNotFound", err)
	}
}

// TestMigrateAddsLogHWMToAnOlderDatabase mimics upgrading a database created before log_hwm existed.
func TestMigrateAddsLogHWMToAnOlderDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE nodes (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, relay_address TEXT NOT NULL DEFAULT '',
		token_hash TEXT NOT NULL DEFAULT '', token_expires_at TEXT NOT NULL DEFAULT '',
		cert_generation INTEGER NOT NULL DEFAULT 0, cert_not_after TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, registered_at TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes(id,name,created_at) VALUES('edge-1','Edge 1','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := New(db)
	got, err := s.GetNode(ctx, "edge-1")
	if err != nil || got.LogHWM != 0 {
		t.Fatalf("migrated node LogHWM = %d, %v, want 0", got.LogHWM, err)
	}
	if err := s.UpdateNodeLogHWM(ctx, "edge-1", 3); err != nil {
		t.Fatal(err)
	}
	// Migrate must be idempotent (it runs on every startup) and must not disturb the column it already added.
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetNode(ctx, "edge-1"); err != nil || got.LogHWM != 3 {
		t.Fatalf("LogHWM after re-running Migrate = %d, %v, want 3", got.LogHWM, err)
	}
}
