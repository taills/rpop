package accesslog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const elasticsearchMaxResponseBytes = 32 << 20

type elasticsearchSink struct {
	config   ElasticsearchConfig
	baseURL  *url.URL
	location *time.Location
	client   *http.Client
}

func newElasticsearchSink(config ElasticsearchConfig) (*elasticsearchSink, error) {
	config = normalizeElasticsearchConfig(config)
	if err := (Config{Adapter: "elasticsearch", Elasticsearch: config}).Validate(); err != nil {
		return nil, err
	}
	baseURL, err := url.Parse(config.URL)
	if err != nil {
		return nil, fmt.Errorf("parse elasticsearch URL: %w", err)
	}
	return &elasticsearchSink{
		config:   config,
		baseURL:  baseURL,
		location: time.UTC,
		client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (s *elasticsearchSink) SetTimeZone(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	s.location = location
}

func (s *elasticsearchSink) indexForTimestamp(timestamp time.Time) string {
	switch s.config.SplitMode {
	case "day":
		return s.config.Index + "-" + timestamp.In(s.location).Format("20060102")
	case "hour":
		return s.config.Index + "-" + timestamp.In(s.location).Format("2006010215")
	default:
		return s.config.Index
	}
}

func (s *elasticsearchSink) endpointFor(index string, operation ...string) (string, error) {
	parts := append([]string{index}, operation...)
	endpoint, err := url.JoinPath(s.baseURL.String(), parts...)
	if err != nil {
		return "", fmt.Errorf("build elasticsearch endpoint: %w", err)
	}
	return endpoint, nil
}

func (s *elasticsearchSink) request(ctx context.Context, method, endpoint, contentType string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	switch s.config.AuthType {
	case "basic":
		request.SetBasicAuth(s.config.Username, s.config.Password)
	case "apiKey":
		request.Header.Set("Authorization", "ApiKey "+s.config.APIKey)
	}

	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, elasticsearchMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > elasticsearchMaxResponseBytes {
		return nil, fmt.Errorf("elasticsearch response exceeds %d bytes", elasticsearchMaxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("elasticsearch returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (s *elasticsearchSink) Write(ctx context.Context, record Record) error {
	normalizeRecordTime(&record)
	endpoint, err := s.endpointFor(s.indexForTimestamp(record.Timestamp), "_bulk")
	if err != nil {
		return err
	}
	// An explicit _id equal to the record's dedup_key (D29) makes a retransmitted record's bulk index action
	// overwrite the same document instead of creating a second one — Elasticsearch already treats indexing the
	// same _id twice as an update, so this needs no code beyond setting it. A record with no dedup_key (no
	// trackId; see Record.DedupKey) falls back to Elasticsearch auto-generating one, exactly like before this
	// change, since an empty _id is not a valid document id.
	action := map[string]any{}
	if dedupKey := record.DedupKey(); dedupKey != "" {
		action["_id"] = dedupKey
	}
	metadata, err := json.Marshal(map[string]any{"index": action})
	if err != nil {
		return err
	}
	document, err := json.Marshal(record)
	if err != nil {
		return err
	}
	body := make([]byte, 0, len(metadata)+len(document)+2)
	body = append(body, metadata...)
	body = append(body, '\n')
	body = append(body, document...)
	body = append(body, '\n')
	response, err := s.request(ctx, http.MethodPost, endpoint, "application/x-ndjson", body)
	if err != nil {
		return err
	}
	var bulkResult struct {
		Errors bool                         `json:"errors"`
		Items  []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response, &bulkResult); err != nil {
		return fmt.Errorf("decode elasticsearch bulk response: %w", err)
	}
	if !bulkResult.Errors {
		return nil
	}
	for _, item := range bulkResult.Items {
		for operation, raw := range item {
			var result struct {
				Status int `json:"status"`
				Error  struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				continue
			}
			return fmt.Errorf("elasticsearch bulk %s failed (status %d, %s): %s", operation, result.Status, result.Error.Type, result.Error.Reason)
		}
	}
	return fmt.Errorf("elasticsearch bulk request reported item errors")
}

func (s *elasticsearchSink) Search(ctx context.Context, query Query) (SearchResult, error) {
	query = normalizeQuery(query)
	maxInt := int(^uint(0) >> 1)
	if query.Page-1 > maxInt/query.PageSize {
		return SearchResult{}, fmt.Errorf("elasticsearch page offset is too large")
	}
	boolQuery := make(map[string]any)
	filters := make([]any, 0, 4)
	if query.SiteID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"siteId.keyword": query.SiteID}})
	}
	if query.TrackID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"trackId.keyword": query.TrackID}})
	}
	if query.Status > 0 {
		filters = append(filters, map[string]any{"term": map[string]any{"status": query.Status}})
	}
	dateRange := make(map[string]string, 2)
	if !query.From.IsZero() {
		dateRange["gte"] = query.From.UTC().Format(time.RFC3339Nano)
	}
	if !query.To.IsZero() {
		dateRange["lte"] = query.To.UTC().Format(time.RFC3339Nano)
	}
	if len(dateRange) > 0 {
		filters = append(filters, map[string]any{"range": map[string]any{"timestamp": dateRange}})
	}
	if len(filters) > 0 {
		boolQuery["filter"] = filters
	}
	if query.Text != "" {
		boolQuery["must"] = []any{map[string]any{"multi_match": map[string]any{
			"query":  query.Text,
			"fields": []string{"path", "siteId", "method", "clientIp", "forwardedFor", "host", "referer", "userAgent", "upstream", "route", "requestBody", "responseBody", "requestHeaders.*", "responseHeaders.*"},
		}}}
	}
	searchBody, err := json.Marshal(map[string]any{
		"from":             (query.Page - 1) * query.PageSize,
		"size":             query.PageSize,
		"track_total_hits": true,
		"query":            map[string]any{"bool": boolQuery},
		"sort":             []any{map[string]any{"timestamp": map[string]string{"order": "desc"}}},
	})
	if err != nil {
		return SearchResult{}, err
	}
	// Always include the configured base index and suffixed indices so searches
	// remain complete when an adapter changes its split mode over time.
	searchIndex := s.config.Index + "," + s.config.Index + "-*"
	endpoint, err := s.endpointFor(searchIndex, "_search")
	if err != nil {
		return SearchResult{}, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return SearchResult{}, err
	}
	values := parsed.Query()
	values.Set("allow_no_indices", "true")
	values.Set("ignore_unavailable", "true")
	parsed.RawQuery = values.Encode()
	endpoint = parsed.String()
	response, err := s.request(ctx, http.MethodPost, endpoint, "application/json", searchBody)
	if err != nil {
		return SearchResult{}, err
	}
	var searchResponse struct {
		Hits struct {
			Total json.RawMessage `json:"total"`
			Hits  []struct {
				Source Record `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(response, &searchResponse); err != nil {
		return SearchResult{}, fmt.Errorf("decode elasticsearch search response: %w", err)
	}
	total, err := parseElasticsearchTotal(searchResponse.Hits.Total)
	if err != nil {
		return SearchResult{}, err
	}
	records := make([]Record, 0, len(searchResponse.Hits.Hits))
	for _, hit := range searchResponse.Hits.Hits {
		records = append(records, hit.Source)
	}
	return SearchResult{Records: records, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func parseElasticsearchTotal(raw json.RawMessage) (int, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var total struct {
			Value int `json:"value"`
		}
		if err := json.Unmarshal(raw, &total); err != nil {
			return 0, fmt.Errorf("decode elasticsearch hit total: %w", err)
		}
		return total.Value, nil
	}
	total, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("decode elasticsearch hit total: %w", err)
	}
	return total, nil
}

func (s *elasticsearchSink) Close() error { return nil }
