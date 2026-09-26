package accesslog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type clickHouseSink struct {
	config     ClickHouseConfig
	client     *http.Client
	mu         sync.Mutex
	tableReady bool
}

func newClickHouseSink(config ClickHouseConfig) (*clickHouseSink, error) {
	return &clickHouseSink{config: config, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (s *clickHouseSink) ensureTable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tableReady {
		return nil
	}
	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`.`%s` (timestamp DateTime64(3, 'UTC'), site_id String, record String) ENGINE=MergeTree PARTITION BY toDate(timestamp) ORDER BY (timestamp, site_id)", s.config.Database, s.config.Table)
	if _, err := s.request(ctx, query, nil); err != nil {
		return err
	}
	s.tableReady = true
	return nil
}

func (s *clickHouseSink) request(ctx context.Context, query string, body []byte) ([]byte, error) {
	endpoint, err := url.Parse(s.config.URL)
	if err != nil {
		return nil, err
	}
	values := endpoint.Query()
	values.Set("database", s.config.Database)
	values.Set("query", query)
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if s.config.Username != "" {
		request.SetBasicAuth(s.config.Username, s.config.Password)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("clickhouse returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (s *clickHouseSink) Write(ctx context.Context, record Record) error {
	if err := s.ensureTable(ctx); err != nil {
		return err
	}
	normalizeRecordTime(&record)
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	row, err := json.Marshal(struct {
		Timestamp string `json:"timestamp"`
		SiteID    string `json:"site_id"`
		Record    string `json:"record"`
	}{record.Timestamp.Format("2006-01-02 15:04:05.000"), record.SiteID, string(raw)})
	if err != nil {
		return err
	}
	_, err = s.request(ctx, fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT JSONEachRow", s.config.Database, s.config.Table), append(row, '\n'))
	return err
}

func (s *clickHouseSink) Search(ctx context.Context, query Query) (SearchResult, error) {
	if err := s.ensureTable(ctx); err != nil {
		return SearchResult{}, err
	}
	where := []string{"1"}
	if query.SiteID != "" {
		where = append(where, "site_id='"+sqlQuote(query.SiteID)+"'")
	}
	if query.Status > 0 {
		where = append(where, "JSONExtractInt(record, 'status')="+fmt.Sprint(query.Status))
	}
	if !query.From.IsZero() {
		where = append(where, "timestamp >= toDateTime64('"+query.From.UTC().Format("2006-01-02 15:04:05.000")+"',3,'UTC')")
	}
	if !query.To.IsZero() {
		where = append(where, "timestamp <= toDateTime64('"+query.To.UTC().Format("2006-01-02 15:04:05.000")+"',3,'UTC')")
	}
	if query.Text != "" {
		where = append(where, "positionCaseInsensitive(record, '"+sqlQuote(query.Text)+"') > 0")
	}
	predicate := strings.Join(where, " AND ")
	countData, err := s.request(ctx, "SELECT count() FROM `"+s.config.Database+"`.`"+s.config.Table+"` WHERE "+predicate+" FORMAT TabSeparated", nil)
	if err != nil {
		return SearchResult{}, err
	}
	total := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(countData)), "%d", &total); err != nil {
		return SearchResult{}, err
	}
	offset := (query.Page - 1) * query.PageSize
	querySQL := fmt.Sprintf("SELECT record FROM `%s`.`%s` WHERE %s ORDER BY timestamp DESC LIMIT %d OFFSET %d FORMAT TabSeparatedRaw", s.config.Database, s.config.Table, predicate, query.PageSize, offset)
	data, err := s.request(ctx, querySQL, nil)
	if err != nil {
		return SearchResult{}, err
	}
	var records []Record
	reader := bufio.NewReader(bytes.NewReader(data))
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return SearchResult{}, err
			}
			records = append(records, record)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return SearchResult{}, readErr
		}
	}
	if records == nil {
		records = []Record{}
	}
	return SearchResult{Records: records, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func sqlQuote(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "\\'")
}
func (s *clickHouseSink) Close() error { return nil }
