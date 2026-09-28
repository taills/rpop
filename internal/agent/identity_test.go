package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHealthyReflectsWhetherRegistrationEverCompleted exercises the "-health-check" signal for node mode (see
// Healthy's doc comment): unhealthy before the identity files exist, unhealthy if one is truncated, healthy once
// all three hold content.
func TestHealthyReflectsWhetherRegistrationEverCompleted(t *testing.T) {
	dir := t.TempDir()
	if err := Healthy(dir); err == nil {
		t.Fatal("expected an error before any identity file exists")
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(certFile, "cert")
	if err := Healthy(dir); err == nil {
		t.Fatal("expected an error while the key and CA are still missing")
	}
	write(keyFile, "key")
	write(caFile, "")
	if err := Healthy(dir); err == nil {
		t.Fatal("expected an error while ca.crt is empty")
	}
	write(caFile, "ca")
	if err := Healthy(dir); err != nil {
		t.Fatalf("expected no error once every identity file holds content: %v", err)
	}
}

// TestHealthyRejectsAMissingDataDir covers the common first-boot case: -data-dir does not exist yet because the
// node has never run.
func TestHealthyRejectsAMissingDataDir(t *testing.T) {
	if err := Healthy(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a data dir that does not exist")
	}
}
