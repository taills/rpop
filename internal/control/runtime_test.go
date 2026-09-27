package control

import (
	"context"
	"testing"

	"github.com/rpop-project/rpop/internal/accesslog"
)

// TestRegistryWriterStampsReportedByWithTheLocalNodeID covers stage 5 security review item 3 for the embedded
// node's direct write path (item 4 of stage 5 step 3): registryWriter is only ever wired to the controller's own
// in-process dataplane.Engine, which applies exclusively the embedded node's snapshot (see publish.go), so every
// record it sees was genuinely produced locally and must be stamped LocalNodeID, overwriting anything already
// set (mirroring the southbound ingest path's override in ingestLogLine).
func TestRegistryWriterStampsReportedByWithTheLocalNodeID(t *testing.T) {
	registry, err := accesslog.NewRegistry(t.TempDir(), []accesslog.AdapterConfig{{ID: "default", Name: "default", Config: accesslog.DefaultConfig()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	writer := registryWriter{registry: registry}
	record := accesslog.Record{SiteID: "site-a", Path: "/x", ReportedBy: "someone-else"}
	if err := writer.WriteAccessLog(context.Background(), "default", record); err != nil {
		t.Fatal(err)
	}

	result, err := registry.Search(context.Background(), "default", accesslog.Query{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].ReportedBy != LocalNodeID {
		t.Fatalf("stored records = %#v, want exactly one record with ReportedBy=%q", result.Records, LocalNodeID)
	}
}
