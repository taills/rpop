package accesslog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestRegistryAppliesSystemTimeZoneToExistingAndAddedAdapters(t *testing.T) {
	clickHouseConfig := Config{Adapter: "clickhouse", ClickHouse: ClickHouseConfig{
		URL: "http://localhost:8123", Database: "default", Table: "access_logs", SplitMode: "hour",
	}}
	s3Config := Config{Adapter: "s3", S3: S3Config{
		Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "logs", Prefix: "rpop/access",
		AccessKeyID: "test-key", SecretAccessKey: "test-secret", SplitMode: "hour", ForcePathStyle: true,
	}}
	registry, err := NewRegistry(t.TempDir(), []AdapterConfig{
		{ID: "file", Name: "Local file", Config: DefaultConfig()},
		{ID: "clickhouse", Name: "ClickHouse", Config: clickHouseConfig},
		{ID: "s3", Name: "S3", Config: s3Config},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	registry.SetTimeZone(location)
	timestamp := time.Date(2026, 9, 27, 6, 14, 0, 0, time.UTC)

	fileSink := registry.adapters["file"].manager.sink.(*fileSink)
	if got := rotationPeriod("day", timestamp, fileSink.location); got != "20260926" {
		t.Fatalf("file rotation did not use system timezone: %s", got)
	}
	clickHouseSink := registry.adapters["clickhouse"].manager.sink.(*clickHouseSink)
	if got, want := clickHouseSink.tableForTimestamp(timestamp), "access_logs_2026092623"; got != want {
		t.Fatalf("ClickHouse partition did not use system timezone: got %q, want %q", got, want)
	}
	s3Sink := registry.adapters["s3"].manager.sink.(*s3Sink)
	if got := s3Sink.objectKey(Record{Timestamp: timestamp}, "test"); !strings.HasPrefix(got, "rpop/access/20260926/23/") {
		t.Fatalf("S3 partition did not use system timezone: %q", got)
	}

	elasticsearchConfig := Config{Adapter: "elasticsearch", Elasticsearch: ElasticsearchConfig{
		URL: "http://localhost:9200", Index: "access-logs", SplitMode: "hour", AuthType: "none",
	}}
	if err := registry.Add(AdapterConfig{ID: "elasticsearch", Name: "Elasticsearch", Config: elasticsearchConfig}, nil); err != nil {
		t.Fatal(err)
	}
	elasticsearchSink := registry.adapters["elasticsearch"].manager.sink.(*elasticsearchSink)
	if got, want := elasticsearchSink.indexForTimestamp(timestamp), "access-logs-2026092623"; got != want {
		t.Fatalf("new Elasticsearch adapter did not inherit system timezone: got %q, want %q", got, want)
	}
}
