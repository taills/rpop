package accesslog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestElasticsearchAdapterBulkWritesAndSearchesRecords(t *testing.T) {
	var written Record
	var bulkRequestSeen, searchRequestSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "rpop" || password != "test-secret" {
			t.Errorf("missing Elasticsearch Basic authentication")
		}
		switch r.URL.Path {
		case "/proxy/rpop-access-logs-2026092602/_bulk":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-ndjson" {
				t.Errorf("unexpected bulk request: method=%s content-type=%s", r.Method, r.Header.Get("Content-Type"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
			if len(lines) != 2 {
				t.Errorf("bulk request should contain metadata and one record line, got %d lines", len(lines))
				return
			}
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal([]byte(lines[0]), &metadata); err != nil || metadata["index"] == nil {
				t.Errorf("invalid bulk action metadata: %s (%v)", lines[0], err)
			}
			if err := json.Unmarshal([]byte(lines[1]), &written); err != nil {
				t.Error(err)
				return
			}
			bulkRequestSeen = true
			_, _ = io.WriteString(w, `{"errors":false,"items":[{"index":{"status":201}}]}`)
		case "/proxy/rpop-access-logs,rpop-access-logs-*/_search":
			if r.URL.Query().Get("allow_no_indices") != "true" || r.URL.Query().Get("ignore_unavailable") != "true" {
				t.Errorf("partitioned search should ignore missing indices: %s", r.URL.RawQuery)
			}
			var query map[string]any
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Error(err)
				return
			}
			if query["from"] != float64(10) || query["size"] != float64(10) || query["track_total_hits"] != true {
				t.Errorf("unexpected pagination/search options: %#v", query)
			}
			encoded, _ := json.Marshal(query)
			for _, expected := range []string{"siteId.keyword", "status", "timestamp", "multi_match", "/ingest"} {
				if !strings.Contains(string(encoded), expected) {
					t.Errorf("search query missing %q: %s", expected, encoded)
				}
			}
			searchRequestSeen = true
			_, _ = fmt.Fprintf(w, `{"hits":{"total":{"value":2,"relation":"eq"},"hits":[{"_source":%s}]}}`, mustJSON(t, written))
		default:
			t.Errorf("unexpected Elasticsearch request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	sink, err := newElasticsearchSink(ElasticsearchConfig{
		URL: server.URL + "/proxy", Index: "rpop-access-logs", SplitMode: "hour", AuthType: "basic", Username: "rpop", Password: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	sink.SetTimeZone(location)
	defer sink.Close()
	timestamp := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	record := Record{
		Timestamp: timestamp, SiteID: "site-search", Method: "POST", Path: "/ingest", Protocol: "HTTP/1.1",
		RequestHeaders: map[string][]string{"Content-Type": {"application/json"}}, Status: http.StatusAccepted,
		ResponseHeaders: map[string][]string{"X-Result": {"ok"}}, RequestBody: `{"item":"value"}`, RequestBytes: 18,
		ResponseBytes: 2, TTFBMillis: 1.5, ResponseMillis: 3.0,
	}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if !bulkRequestSeen || written.Path != record.Path || written.SiteID != record.SiteID || written.Timestamp != record.Timestamp {
		t.Fatalf("bulk write did not preserve the record: seen=%v record=%#v", bulkRequestSeen, written)
	}
	result, err := sink.Search(context.Background(), Query{
		Text: "/ingest", SiteID: "site-search", Status: http.StatusAccepted,
		From: timestamp.Add(-time.Minute), To: timestamp.Add(time.Minute), Page: 2, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !searchRequestSeen || result.Total != 2 || len(result.Records) != 1 || result.Page != 2 || result.PageSize != 10 || result.Records[0].Path != record.Path {
		t.Fatalf("unexpected Elasticsearch search result: seen=%v result=%#v", searchRequestSeen, result)
	}
}

func TestElasticsearchAdapterUsesAPIKeyHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "ApiKey dGVzdC1pZDpkZW1vLWtleQ==" {
			t.Errorf("unexpected API key header: %q", got)
		}
		_, _ = io.WriteString(w, `{"errors":false,"items":[{"index":{"status":201}}]}`)
	}))
	defer server.Close()
	sink, err := newElasticsearchSink(ElasticsearchConfig{URL: server.URL, Index: "rpop-test", AuthType: "apiKey", APIKey: "dGVzdC1pZDpkZW1vLWtleQ=="})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if err := sink.Write(context.Background(), Record{Timestamp: time.Now().UTC(), SiteID: "site-a", Status: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestElasticsearchSearchIncludesHistoricalPartitionsInNoSplitMode(t *testing.T) {
	timestamp := time.Date(2026, 9, 26, 9, 14, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpop-access-logs,rpop-access-logs-*/_search" {
			t.Errorf("search omitted suffixed indices: %s", r.URL.Path)
		}
		if r.URL.Query().Get("allow_no_indices") != "true" || r.URL.Query().Get("ignore_unavailable") != "true" {
			t.Errorf("search should tolerate missing partitions: %s", r.URL.RawQuery)
		}
		_, _ = fmt.Fprintf(w, `{"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_source":%s}]}}`, mustJSON(t, Record{Timestamp: timestamp, SiteID: "site-old", Path: "/old-partition"}))
	}))
	defer server.Close()
	sink, err := newElasticsearchSink(ElasticsearchConfig{URL: server.URL, Index: "rpop-access-logs", SplitMode: "none", AuthType: "none"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := sink.Search(context.Background(), Query{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].Path != "/old-partition" {
		t.Fatalf("historical partition was not returned: %#v", result)
	}
}

func TestElasticsearchConfigValidation(t *testing.T) {
	valid := ElasticsearchConfig{URL: "https://es.example.test:9200", Index: "rpop-access-logs", AuthType: "none"}
	cases := []struct {
		name   string
		config ElasticsearchConfig
		valid  bool
	}{
		{name: "no auth", config: valid, valid: true},
		{name: "daily index split", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, SplitMode: "day", AuthType: "none"}, valid: true},
		{name: "hourly index split", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, SplitMode: "hour", AuthType: "none"}, valid: true},
		{name: "invalid split mode", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, SplitMode: "week", AuthType: "none"}},
		{name: "hourly suffix exceeds index length", config: ElasticsearchConfig{URL: valid.URL, Index: strings.Repeat("a", 245), SplitMode: "hour", AuthType: "none"}},
		{name: "basic auth", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, AuthType: "basic", Username: "rpop", Password: "secret"}, valid: true},
		{name: "api key", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, AuthType: "apiKey", APIKey: "encoded-key"}, valid: true},
		{name: "invalid scheme", config: ElasticsearchConfig{URL: "ftp://es.example.test", Index: valid.Index, AuthType: "none"}},
		{name: "url credentials", config: ElasticsearchConfig{URL: "https://user:password@es.example.test", Index: valid.Index, AuthType: "none"}},
		{name: "query in url", config: ElasticsearchConfig{URL: "https://es.example.test?x=1", Index: valid.Index, AuthType: "none"}},
		{name: "uppercase index", config: ElasticsearchConfig{URL: valid.URL, Index: "Rpop-Logs", AuthType: "none"}},
		{name: "invalid index character", config: ElasticsearchConfig{URL: valid.URL, Index: "rpop/logs", AuthType: "none"}},
		{name: "missing basic password", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, AuthType: "basic", Username: "rpop"}},
		{name: "conflicting credentials", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, AuthType: "apiKey", Username: "rpop", APIKey: "key"}},
		{name: "unknown auth type", config: ElasticsearchConfig{URL: valid.URL, Index: valid.Index, AuthType: "bearer"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (Config{Adapter: "elasticsearch", Elasticsearch: tc.config}).Validate()
			if tc.valid && err != nil {
				t.Fatalf("expected valid config, got %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("expected invalid config")
			}
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
