package accesslog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// These tests cover D29 (docs/architecture/control-data-plane.md §5, "阶段 7(长尾)设计"): a record or tunnel
// event retransmitted by the same segment being redelivered (D24) must not appear twice to a caller, even though
// nothing stops it from landing on disk (or in ClickHouse/Elasticsearch) more than once.

func TestRecordDedupKeyIsTrackID(t *testing.T) {
	if got := (Record{TrackID: "t-1"}).DedupKey(); got != "t-1" {
		t.Fatalf("DedupKey() = %q, want %q", got, "t-1")
	}
	if got := (Record{}).DedupKey(); got != "" {
		t.Fatalf("DedupKey() with no trackId = %q, want empty", got)
	}
}

func TestDedupRecordsKeepsFirstOccurrenceAndNeverMergesEmptyKeys(t *testing.T) {
	records := []Record{
		{TrackID: "t-1", Path: "/first"},
		{TrackID: "t-1", Path: "/retransmit"},
		{TrackID: "", Path: "/no-track-a"},
		{TrackID: "", Path: "/no-track-b"},
		{TrackID: "t-2", Path: "/other"},
	}
	got := dedupRecords(records)
	if len(got) != 4 {
		t.Fatalf("dedupRecords returned %d records, want 4: %#v", len(got), got)
	}
	paths := make([]string, len(got))
	for i, r := range got {
		paths[i] = r.Path
	}
	want := []string{"/first", "/no-track-a", "/no-track-b", "/other"}
	for _, w := range want {
		found := false
		for _, p := range paths {
			if p == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("dedupRecords result %v missing %q (empty-key records must never be merged with each other)", paths, w)
		}
	}
	for _, p := range paths {
		if p == "/retransmit" {
			t.Fatalf("dedupRecords result %v kept the retransmitted duplicate instead of collapsing it", paths)
		}
	}
}

// TestFileSearchDeduplicatesRetransmittedRecords covers the file adapter's half of D29's query-time dedup: the
// same trackId written twice (as a segment redelivery would, D24) must collapse to one record in Search, while a
// record with no trackId at all is never merged with anything, and Total must reflect the deduplicated count, not
// the raw one — there is no earlier LIMIT for paginate to interact with here (see dedupRecords' doc comment).
func TestFileSearchDeduplicatesRetransmittedRecords(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, Config{Adapter: "file"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	retransmitted := Record{Timestamp: base, SiteID: "site-a", TrackID: "track-retransmit", Path: "/retransmit", RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}}
	noTrack := Record{Timestamp: base.Add(time.Second), SiteID: "site-a", Path: "/no-track", RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}}
	for _, record := range []Record{retransmitted, retransmitted, noTrack, noTrack} {
		if err := manager.Write(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.Search(context.Background(), Query{SiteID: "site-a", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 1 deduplicated "/retransmit" + 2 "/no-track" copies (never merged: no trackId identifies them).
	if result.Total != 3 || len(result.Records) != 3 {
		t.Fatalf("Search = %#v, want Total 3 (retransmitted record collapsed, empty-trackId records kept)", result)
	}
	retransmitCount := 0
	for _, r := range result.Records {
		if r.Path == "/retransmit" {
			retransmitCount++
		}
	}
	if retransmitCount != 1 {
		t.Fatalf("got %d copies of the retransmitted record, want exactly 1: %#v", retransmitCount, result.Records)
	}
}

// TestS3SearchDeduplicatesRetransmittedRecords covers the S3 adapter's half of the same behavior: unlike the file
// adapter, each Write of "the same" record lands under its own randomly suffixed object key (see s3Sink.Write),
// so two retransmitted writes are two distinct objects on disk — exactly the shape D29 says the S3 adapter must
// tolerate without rewriting anything already stored, deduplicating only in Search's in-memory aggregation.
func TestS3SearchDeduplicatesRetransmittedRecords(t *testing.T) {
	var objects [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects = append(objects, body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				var entries strings.Builder
				for i := range objects {
					entries.WriteString("<Contents><Key>object-" + strconv.Itoa(i) + "</Key></Contents>")
				}
				_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`+entries.String()+`</ListBucketResult>`)
				return
			}
			index, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/dedup-bucket/object-"))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(objects[index])
		default:
			t.Errorf("unexpected S3 method %s", r.Method)
		}
	}))
	defer server.Close()
	sink, err := newS3Sink(S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "dedup-bucket", AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Date(2026, 9, 26, 9, 14, 0, 0, time.UTC)
	record := Record{Timestamp: timestamp, SiteID: "site-c", Method: "GET", Path: "/s3-retransmit", Status: 200, TrackID: "track-s3-retransmit"}
	// Two writes of the identical record, exactly like a redelivered segment (D24): each lands under its own
	// object key, so there really are two copies on disk.
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected two distinct S3 objects for the two writes, got %d", len(objects))
	}
	result, err := sink.Search(context.Background(), Query{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].Path != "/s3-retransmit" {
		t.Fatalf("Search = %#v, want the two on-disk copies collapsed to one", result)
	}
}

// TestClickHouseEnsureTableUsesReplacingMergeTreeWithDedupKeyColumn covers D29's DDL contract directly: the
// CREATE statement must add dedup_key, switch the engine to ReplacingMergeTree, and order by
// (timestamp, site_id, dedup_key) so ClickHouse can actually collapse a retransmitted row.
func TestClickHouseEnsureTableUsesReplacingMergeTreeWithDedupKeyColumn(t *testing.T) {
	var createQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query := r.URL.Query().Get("query"); strings.HasPrefix(query, "CREATE TABLE") {
			createQuery = query
			return
		}
		_, _ = io.WriteString(w, "ReplacingMergeTree\n")
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.ensureTable(context.Background(), "access_logs"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dedup_key String", "ENGINE=ReplacingMergeTree", "ORDER BY (timestamp, site_id, dedup_key)"} {
		if !strings.Contains(createQuery, want) {
			t.Fatalf("CREATE TABLE %q missing %q", createQuery, want)
		}
	}
}

// TestClickHouseWriteFillsDedupKeyFromTheRecordDirectly covers the "not by parsing JSON" half of the write-side
// contract: dedup_key must come from the Go struct field already in hand, not from re-parsing the record it just
// marshaled.
func TestClickHouseWriteFillsDedupKeyFromTheRecordDirectly(t *testing.T) {
	var insertedDedupKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if strings.HasPrefix(query, "INSERT") {
			var row struct {
				DedupKey string `json:"dedup_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
				t.Error(err)
				return
			}
			insertedDedupKey = row.DedupKey
			return
		}
		if strings.HasPrefix(query, "SELECT engine FROM system.tables") {
			_, _ = io.WriteString(w, "ReplacingMergeTree\n")
		}
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs"})
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Timestamp: time.Now().UTC(), SiteID: "site-a", TrackID: "track-clickhouse", Status: 200}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if insertedDedupKey != "track-clickhouse" {
		t.Fatalf("inserted dedup_key = %q, want %q", insertedDedupKey, "track-clickhouse")
	}
}

// TestClickHouseSearchAddsFinalToEveryPartitionSubquery covers Search's read-side fallback: FINAL forces
// ClickHouse to apply ReplacingMergeTree's deduplication at query time rather than only whenever a background
// merge happens to have already run.
func TestClickHouseSearchAddsFinalToEveryPartitionSubquery(t *testing.T) {
	var recordsQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch {
		case strings.HasPrefix(query, "SELECT name FROM system.tables"):
			_, _ = io.WriteString(w, "access_logs\naccess_logs_20260926\n")
		case strings.Contains(query, "count()"):
			_, _ = io.WriteString(w, "0\n")
		case strings.Contains(query, "SELECT record"):
			recordsQuery = query
		default:
			t.Errorf("unexpected ClickHouse query: %q", query)
		}
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs", SplitMode: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Search(context.Background(), Query{Page: 1, PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recordsQuery, "access_logs` FINAL") || !strings.Contains(recordsQuery, "access_logs_20260926` FINAL") {
		t.Fatalf("Search did not add FINAL to every partition subquery: %q", recordsQuery)
	}
}

// TestClickHouseWarnsOnceWhenATablePredatesDedupSupport covers the operator-facing half of the migration story
// (D29): a table that already existed under the old MergeTree engine must log a clear Warn the first time this
// sink notices it, and never a second time for the same table (tablesReady already prevents re-checking it).
func TestClickHouseWarnsOnceWhenATablePredatesDedupSupport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if strings.HasPrefix(query, "SELECT engine FROM system.tables") {
			_, _ = io.WriteString(w, "MergeTree\n")
		}
	}))
	defer server.Close()
	sink, err := newClickHouseSink(ClickHouseConfig{URL: server.URL, Database: "default", Table: "access_logs"})
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.WarnLevel)
	sink.SetLogger(zap.New(core))

	if err := sink.ensureTable(context.Background(), "access_logs"); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 1 {
		t.Fatalf("expected exactly one Warn for a legacy table, got %d: %v", logs.Len(), logs.All())
	}
	// A second ensureTable for the same table must not re-check or re-warn (tablesReady caches it).
	if err := sink.ensureTable(context.Background(), "access_logs"); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 1 {
		t.Fatalf("expected the Warn to fire only once, got %d: %v", logs.Len(), logs.All())
	}
}

// TestElasticsearchBulkUsesDedupKeyAsDocumentID covers the Elasticsearch half of D29: a record with a trackId
// gets that trackId as its bulk _id (so a retransmitted write overwrites the same document instead of creating a
// second one), while a record with no trackId falls back to Elasticsearch auto-generating one, exactly as before.
func TestElasticsearchBulkUsesDedupKeyAsDocumentID(t *testing.T) {
	var actions []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal([]byte(lines[0]), &metadata); err != nil {
			t.Fatal(err)
		}
		var indexAction map[string]json.RawMessage
		if err := json.Unmarshal(metadata["index"], &indexAction); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, indexAction)
		_, _ = io.WriteString(w, `{"errors":false,"items":[{"index":{"status":201}}]}`)
	}))
	defer server.Close()
	sink, err := newElasticsearchSink(ElasticsearchConfig{URL: server.URL, Index: "rpop-access-logs", AuthType: "none"})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	if err := sink.Write(context.Background(), Record{Timestamp: time.Now().UTC(), SiteID: "site-a", TrackID: "track-es-dedup", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), Record{Timestamp: time.Now().UTC(), SiteID: "site-a", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected two bulk actions, got %d", len(actions))
	}
	var withTrack string
	if err := json.Unmarshal(actions[0]["_id"], &withTrack); err != nil || withTrack != "track-es-dedup" {
		t.Fatalf("first bulk action _id = %s (err %v), want %q", actions[0]["_id"], err, "track-es-dedup")
	}
	if _, ok := actions[1]["_id"]; ok {
		t.Fatalf("second bulk action (no trackId) must omit _id and let Elasticsearch auto-generate one, got %s", actions[1]["_id"])
	}
}
