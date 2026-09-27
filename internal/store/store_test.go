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
