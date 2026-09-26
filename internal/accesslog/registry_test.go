package accesslog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryAllowsMultipleAdaptersOfSameType(t *testing.T) {
	dir := t.TempDir()
	fileConfig := DefaultConfig()
	registry, err := NewRegistry(dir, []AdapterConfig{
		{ID: "default", Name: "Primary file", Config: fileConfig},
		{ID: "secondary-file", Name: "Secondary file", Config: fileConfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	for _, item := range []struct{ id, site string }{{"default", "primary-site"}, {"secondary-file", "secondary-site"}} {
		record := Record{Timestamp: time.Now().UTC(), SiteID: item.site, Method: "GET", Path: "/" + item.site, Status: 200}
		if err := registry.Write(context.Background(), item.id, record); err != nil {
			t.Fatalf("write to %s: %v", item.id, err)
		}
		result, err := registry.Search(context.Background(), item.id, Query{SiteID: item.site, Page: 1, PageSize: 10})
		if err != nil {
			t.Fatalf("search %s: %v", item.id, err)
		}
		if result.Total != 1 || len(result.Records) != 1 || result.Records[0].SiteID != item.site {
			t.Fatalf("unexpected results for %s: %#v", item.id, result)
		}
	}
	for _, path := range []string{
		filepath.Join(dir, activeFileName),
		filepath.Join(dir, "access-adapters", "secondary-file", activeFileName),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected isolated file adapter path %s: %v", path, err)
		}
	}
	if len(registry.List()) != 2 {
		t.Fatalf("expected two same-type adapters, got %d", len(registry.List()))
	}
}
