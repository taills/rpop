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
	"slices"
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

// TestFileSearchFiltersByTrackID exercises the shared matches() helper (internal/accesslog/manager.go), which
// the file and s3 sinks both call directly: covering it here covers both.
func TestFileSearchFiltersByTrackID(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, Config{Adapter: "file"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	records := []Record{
		{Timestamp: base, SiteID: "site-a", TrackID: "0193f2a4-0000-7000-8000-000000000001", Path: "/one", RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}},
		{Timestamp: base.Add(time.Second), SiteID: "site-a", TrackID: "0193f2a4-0000-7000-8000-000000000002", Path: "/two", RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}},
	}
	for _, record := range records {
		if err := manager.Write(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.Search(context.Background(), Query{TrackID: "0193f2a4-0000-7000-8000-000000000002", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].Path != "/two" {
		t.Fatalf("TrackID search = %#v, want exactly the record for /two", result)
	}
	if empty, err := manager.Search(context.Background(), Query{TrackID: "0193f2a4-0000-7000-8000-00000000ffff", Page: 1, PageSize: 10}); err != nil || empty.Total != 0 {
		t.Fatalf("TrackID search for an unknown id = %#v, %v, want zero results", empty, err)
	}
}

func TestClickHouseSearchFiltersByTrackID(t *testing.T) {
	var lastQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch {
		case strings.HasPrefix(query, "SELECT name FROM system.tables"):
			_, _ = io.WriteString(w, "access_logs\n")
		case strings.Contains(query, "count()"):
			lastQuery = query
			_, _ = io.WriteString(w, "0\n")
		case strings.Contains(query, "SELECT record"):
			lastQuery = query
		default:
			t.Errorf("unexpected ClickHouse query: %q", query)
		}
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Search(context.Background(), Query{TrackID: "0193f2a4-0000-7000-8000-000000000001", Page: 1, PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastQuery, "JSONExtractString(record, 'trackId')='0193f2a4-0000-7000-8000-000000000001'") {
		t.Fatalf("ClickHouse query did not filter by trackId: %q", lastQuery)
	}
}

func TestRemoteAdapterSplitModeValidation(t *testing.T) {
	validClickHouse := Config{Adapter: "clickhouse", ClickHouse: ClickHouseConfig{URL: "http://localhost:8123", Database: "default", Table: "access_logs", SplitMode: "day"}}
	validS3 := Config{Adapter: "s3", S3: S3Config{Region: "us-east-1", Bucket: "logs", AccessKeyID: "key", SecretAccessKey: "secret", SplitMode: "hour"}}
	validElasticsearch := Config{Adapter: "elasticsearch", Elasticsearch: ElasticsearchConfig{URL: "http://localhost:9200", Index: "access-logs", SplitMode: "day", AuthType: "none"}}
	if err := validClickHouse.Validate(); err != nil {
		t.Errorf("valid ClickHouse split mode rejected: %v", err)
	}
	if err := validS3.Validate(); err != nil {
		t.Errorf("valid S3 split mode rejected: %v", err)
	}
	if err := validElasticsearch.Validate(); err != nil {
		t.Errorf("valid Elasticsearch split mode rejected: %v", err)
	}
	invalidClickHouse := validClickHouse
	invalidClickHouse.ClickHouse.SplitMode = "week"
	if err := invalidClickHouse.Validate(); err == nil {
		t.Error("invalid ClickHouse split mode accepted")
	}
	invalidS3 := validS3
	invalidS3.S3.SplitMode = "week"
	if err := invalidS3.Validate(); err == nil {
		t.Error("invalid S3 split mode accepted")
	}
	defaults := normalizeConfig(Config{})
	if defaults.ClickHouse.SplitMode != "none" || defaults.S3.SplitMode != "hour" || defaults.Elasticsearch.SplitMode != "none" {
		t.Errorf("unexpected compatibility defaults: CH=%q S3=%q ES=%q", defaults.ClickHouse.SplitMode, defaults.S3.SplitMode, defaults.Elasticsearch.SplitMode)
	}
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
		case strings.HasPrefix(query, "SELECT name FROM system.tables"):
			_, _ = io.WriteString(w, "access_logs\n")
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

func TestClickHouseAdapterSplitsTablesAndSearchesMatchingPartitions(t *testing.T) {
	var createdQuery, insertQuery, countQuery, recordsQuery string
	inserted := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch {
		case strings.HasPrefix(query, "CREATE TABLE"):
			createdQuery = query
		case strings.HasPrefix(query, "INSERT"):
			insertQuery = query
			var row struct {
				Record string `json:"record"`
			}
			if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
				t.Error(err)
				return
			}
			inserted = row.Record
		case strings.HasPrefix(query, "SELECT name FROM system.tables"):
			_, _ = io.WriteString(w, "access_logs\naccess_logs_20260922\naccess_logs_20260925\naccess_logs_20260926\naccess_logs_2026092609\naccess_logs_2026092611\naccess_logs_20260927\naccess_logs_archive\n")
		case strings.Contains(query, "count()"):
			countQuery = query
			_, _ = io.WriteString(w, "1\n")
		case strings.Contains(query, "SELECT record"):
			recordsQuery = query
			_, _ = io.WriteString(w, inserted+"\n")
		default:
			t.Errorf("unexpected ClickHouse query: %q", query)
		}
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs", SplitMode: "day"})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	if err := sink.Write(context.Background(), Record{Timestamp: timestamp, SiteID: "site-day", Path: "/partitioned", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(createdQuery, "`access_logs_20260926`") || !strings.Contains(insertQuery, "`access_logs_20260926`") {
		t.Fatalf("write did not target the daily table: create=%q insert=%q", createdQuery, insertQuery)
	}
	result, err := sink.Search(context.Background(), Query{From: timestamp, To: timestamp.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{"count": countQuery, "records": recordsQuery} {
		if !strings.Contains(query, "`access_logs_20260926`") || !strings.Contains(query, "`access_logs_2026092609`") || strings.Contains(query, "`access_logs_20260922`") || strings.Contains(query, "`access_logs_archive`") {
			t.Errorf("%s query did not select valid partitions within the widened timezone range: %s", name, query)
		}
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].Path != "/partitioned" {
		t.Fatalf("unexpected partitioned ClickHouse result: %#v", result)
	}
}

func TestClickHousePartitionsUseConfiguredTimeZone(t *testing.T) {
	var createQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		createQuery = r.URL.Query().Get("query")
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs", SplitMode: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	sink.SetTimeZone(location)
	timestamp := time.Date(2026, 9, 27, 6, 14, 0, 0, time.UTC)
	table := sink.tableForTimestamp(timestamp)
	if want := "access_logs_2026092623"; table != want {
		t.Fatalf("timezone-aware table = %q, want %q", table, want)
	}
	if err := sink.ensureTable(context.Background(), table); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(createQuery, "PARTITION BY toDate(timestamp, 'America/Los_Angeles')") {
		t.Fatalf("ClickHouse internal partition ignored the system timezone: %s", createQuery)
	}
}

func TestClickHouseSearchPartitionsUseSystemTimeZone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Query().Get("query"), "SELECT name FROM system.tables") {
			t.Errorf("unexpected ClickHouse query: %q", r.URL.Query().Get("query"))
		}
		_, _ = io.WriteString(w, "access_logs\naccess_logs_20260924\naccess_logs_20260925\naccess_logs_20260926\naccess_logs_20260927\naccess_logs_20260928\naccess_logs_2026092802\n")
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs", SplitMode: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	sink.SetTimeZone(location)
	timestamp := time.Date(2026, 9, 27, 6, 14, 0, 0, time.UTC)
	tables, err := sink.tablesForSearch(context.Background(), Query{From: timestamp, To: timestamp.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tables, "access_logs_20260925") || slices.Contains(tables, "access_logs_20260924") || slices.Contains(tables, "access_logs_2026092802") {
		t.Fatalf("partition search did not use system timezone bounds: %v", tables)
	}
}

func TestS3AdapterSignsUploadsAndSearchesObjects(t *testing.T) {
	var objectKey string
	var listedPrefixes, listedDelimiters []string
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
				listedPrefixes = append(listedPrefixes, r.URL.Query().Get("prefix"))
				listedDelimiters = append(listedDelimiters, r.URL.Query().Get("delimiter"))
				if r.URL.Query().Get("delimiter") == "/" && strings.Contains(strings.TrimPrefix(objectKey, "rpop/test/"), "/") {
					_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
					return
				}
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
	timestamp := time.Date(2026, 9, 26, 9, 14, 0, 0, time.UTC)
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
	hasDayPrefix, hasDirectRootList := false, false
	for i, prefix := range listedPrefixes {
		hasDayPrefix = hasDayPrefix || prefix == "rpop/test/20260926/"
		hasDirectRootList = hasDirectRootList || (prefix == "rpop/test/" && listedDelimiters[i] == "/")
	}
	if !strings.HasPrefix(objectKey, "rpop/test/20260926/09/") || !hasDayPrefix || !hasDirectRootList {
		t.Fatalf("hour split mode was not applied to write/search: object=%q listPrefixes=%v delimiters=%v", objectKey, listedPrefixes, listedDelimiters)
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

func TestS3SearchFindsHistoricalUnsplitObjects(t *testing.T) {
	timestamp := time.Date(2026, 9, 26, 9, 14, 0, 0, time.UTC)
	record := Record{Timestamp: timestamp, SiteID: "legacy-site", Method: "GET", Path: "/legacy-object", Status: 200}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	objectKey := fmt.Sprintf("rpop/access/%d-legacy.jsonl.gz", timestamp.UnixNano())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			if r.URL.Query().Get("prefix") == "rpop/access/" && r.URL.Query().Get("delimiter") == "/" {
				_, _ = fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>%s</Key></Contents></ListBucketResult>`, objectKey)
				return
			}
			_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	sink, err := newS3Sink(S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "logs", Prefix: "rpop/access", SplitMode: "day", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := sink.Search(context.Background(), Query{From: timestamp.Add(-time.Minute), To: timestamp.Add(time.Minute), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].Path != record.Path {
		t.Fatalf("partitioned search missed a historical unsplit object: %#v", result)
	}
}

func TestS3AdapterSplitModesBuildObjectKeys(t *testing.T) {
	timestamp := time.Date(2026, 9, 26, 9, 14, 0, 0, time.UTC)
	filename := fmt.Sprintf("%d-unique.jsonl.gz", timestamp.UnixNano())
	for _, tc := range []struct {
		mode string
		want string
	}{
		{mode: "none", want: "rpop/access/" + filename},
		{mode: "day", want: "rpop/access/20260926/" + filename},
		{mode: "hour", want: "rpop/access/20260926/09/" + filename},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			sink, err := newS3Sink(S3Config{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "logs", Prefix: "rpop/access", SplitMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			got := sink.objectKey(Record{Timestamp: timestamp}, "unique")
			if got != tc.want {
				t.Fatalf("object key = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS3AdapterPartitionsAndSearchesInConfiguredTimeZone(t *testing.T) {
	timestamp := time.Date(2026, 9, 27, 6, 14, 0, 0, time.UTC)
	sink, err := newS3Sink(S3Config{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "logs", Prefix: "rpop/access", SplitMode: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	sink.SetTimeZone(location)
	filename := fmt.Sprintf("%d-unique.jsonl.gz", timestamp.UnixNano())
	if got, want := sink.objectKey(Record{Timestamp: timestamp}, "unique"), "rpop/access/20260926/23/"+filename; got != want {
		t.Fatalf("timezone-aware object key = %q, want %q", got, want)
	}
	prefixes := sink.searchPrefixes(Query{From: timestamp, To: timestamp.Add(time.Minute)})
	if !slices.Contains(prefixes, "rpop/access/20260926/") || !slices.Contains(prefixes, "rpop/access/20260925/") {
		t.Fatalf("search prefixes omitted dates in the configured system timezone: %v", prefixes)
	}
}
