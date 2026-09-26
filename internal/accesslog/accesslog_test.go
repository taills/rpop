package accesslog

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSizeRotationCompressionAndSearch(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, Config{Adapter: "file", File: FileConfig{Rotation: "size", MaxSizeBytes: 400, Compress: true, KeepFiles: 10}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		record := Record{Timestamp: base.Add(time.Duration(i) * time.Second), SiteID: "site-a", Method: "GET", Path: "/search/" + strings.Repeat("x", 180), Status: 200, RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}}
		if err := manager.Write(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	archives, err := filepath.Glob(filepath.Join(dir, "access-*.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) == 0 {
		t.Fatal("expected a gzip archive after size rotation")
	}
	result, err := manager.Search(context.Background(), Query{SiteID: "site-a", Text: "SEARCH", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || len(result.Records) != 2 || result.Records[0].Timestamp.Before(result.Records[1].Timestamp) {
		t.Fatalf("unexpected search result: %#v", result)
	}
	archive, err := os.Open(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(gz); err != nil {
		t.Fatal(err)
	}
	_ = gz.Close()
	_ = archive.Close()
}

func TestClickHouseAdapterWritesAndSearchesJSONRecords(t *testing.T) {
	var inserted string
	var created bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("database") != "default" {
			t.Errorf("database query missing: %s", r.URL.RawQuery)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "admin" || pass != "secret" {
			t.Errorf("basic auth missing")
		}
		query := r.URL.Query().Get("query")
		switch {
		case strings.HasPrefix(query, "CREATE TABLE"):
			created = true
		case strings.HasPrefix(query, "INSERT"):
			var row struct {
				Record string `json:"record"`
			}
			if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
				t.Error(err)
				return
			}
			inserted = row.Record
		case strings.Contains(query, "count()"):
			_, _ = io.WriteString(w, "1\n")
		case strings.Contains(query, "SELECT record"):
			_, _ = io.WriteString(w, inserted+"\n")
		default:
			t.Errorf("unexpected ClickHouse query: %q", query)
		}
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs", Username: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Timestamp: time.Now().UTC(), SiteID: "site-b", Method: "POST", Path: "/ingest", Status: 202}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("ClickHouse table was not initialized")
	}
	result, err := sink.Search(context.Background(), Query{SiteID: "site-b", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].Path != "/ingest" {
		t.Fatalf("unexpected ClickHouse result: %#v", result)
	}
}

func TestS3AdapterSignsUploadsAndSearchesObjects(t *testing.T) {
	var objectKey string
	var object []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test-key/") {
			t.Errorf("missing SigV4 authorization")
		}
		if !strings.HasPrefix(r.URL.Path, "/log-bucket/") {
			t.Errorf("unexpected path-style request: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			objectKey = strings.TrimPrefix(r.URL.Path, "/log-bucket/")
			object, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				_, _ = fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>%s</Key></Contents></ListBucketResult>`, objectKey)
				return
			}
			_, _ = w.Write(object)
		default:
			t.Errorf("unexpected S3 method %s", r.Method)
		}
	}))
	defer server.Close()
	sink, err := newS3Sink(S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "log-bucket", Prefix: "rpop/test", AccessKeyID: "test-key", SecretAccessKey: "test-secret", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().UTC()
	record := Record{Timestamp: timestamp, SiteID: "site-c", Method: "GET", Path: "/s3-search", Status: 200}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if objectKey == "" || len(object) == 0 {
		t.Fatal("S3 object was not uploaded")
	}
	result, err := sink.Search(context.Background(), Query{Text: "s3-search", From: timestamp.Add(-time.Minute), To: timestamp.Add(time.Minute), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].SiteID != "site-c" {
		t.Fatalf("unexpected S3 search: %#v", result)
	}
	reader, err := gzip.NewReader(bytes.NewReader(object))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
}
