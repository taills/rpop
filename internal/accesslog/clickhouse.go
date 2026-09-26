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
	"sort"
	"strings"
	"sync"
	"time"
)

type clickHouseSink struct {
	config      ClickHouseConfig
	client      *http.Client
	location    *time.Location
	mu          sync.Mutex
	tablesReady map[string]bool
}

func newClickHouseSink(config ClickHouseConfig) (*clickHouseSink, error) {
	defaults := DefaultConfig().ClickHouse
	if config.Database == "" {
		config.Database = defaults.Database
	}
	if config.Table == "" {
		config.Table = defaults.Table
	}
	if config.SplitMode == "" {
		config.SplitMode = defaults.SplitMode
	}
	return &clickHouseSink{config: config, client: &http.Client{Timeout: 30 * time.Second}, location: time.UTC, tablesReady: make(map[string]bool)}, nil
}

func (s *clickHouseSink) SetTimeZone(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	s.location = location
}

func (s *clickHouseSink) tableForTimestamp(timestamp time.Time) string {
	switch s.config.SplitMode {
	case "day":
		return s.config.Table + "_" + timestamp.In(s.location).Format("20060102")
	case "hour":
		return s.config.Table + "_" + timestamp.In(s.location).Format("2006010215")
	default:
		return s.config.Table
	}
}

func (s *clickHouseSink) ensureTable(ctx context.Context, table string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tablesReady[table] {
		return nil
	}
	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`.`%s` (timestamp DateTime64(3, 'UTC'), site_id String, record String) ENGINE=MergeTree PARTITION BY toDate(timestamp, '%s') ORDER BY (timestamp, site_id)", s.config.Database, table, sqlQuote(s.location.String()))
	if _, err := s.request(ctx, query, nil); err != nil {
		return err
	}
	s.tablesReady[table] = true
	return nil
}

func (s *clickHouseSink) tablesForSearch(ctx context.Context, query Query) ([]string, error) {
	statement := fmt.Sprintf("SELECT name FROM system.tables WHERE database='%s' AND (name='%s' OR startsWith(name,'%s_')) ORDER BY name FORMAT TabSeparated", sqlQuote(s.config.Database), sqlQuote(s.config.Table), sqlQuote(s.config.Table))
	data, err := s.request(ctx, statement, nil)
	if err != nil {
		return nil, err
	}
	var tables []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lowerSuffix, upperSuffix := "", ""
	if !query.From.IsZero() {
		lowerSuffix = query.From.UTC().Add(-26 * time.Hour).In(s.location).Format("2006010215")
	}
	if !query.To.IsZero() {
		upperSuffix = query.To.UTC().Add(26 * time.Hour).In(s.location).Format("2006010215")
	}
	if !query.From.IsZero() && !query.To.IsZero() {
		// The 26-hour padding can cross a historic date-line transition. When
		// local suffixes are not ordered, keep all partitions and let SQL filter.
		fromDay := query.From.UTC().Add(-26 * time.Hour).In(s.location).Format("20060102")
		toDay := query.To.UTC().Add(26 * time.Hour).In(s.location).Format("20060102")
		if fromDay > toDay {
			lowerSuffix, upperSuffix = "", ""
		}
	}
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == s.config.Table {
			tables = append(tables, name)
			continue
		}
		if !strings.HasPrefix(name, s.config.Table+"_") {
			continue
		}
		suffix := strings.TrimPrefix(name, s.config.Table+"_")
		layout := ""
		switch len(suffix) {
		case 8:
			layout = "20060102"
		case 10:
			layout = "2006010215"
		default:
			continue
		}
		if _, err := time.Parse(layout, suffix); err != nil {
			continue
		}
		// Filter only when local date labels remain ordered; SQL still applies the
		// exact absolute time range to records from candidate partitions.
		if lowerSuffix != "" && suffix < lowerSuffix[:len(layout)] {
			continue
		}
		if upperSuffix != "" && suffix > upperSuffix[:len(layout)] {
			continue
		}
		tables = append(tables, name)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(tables) == 0 && s.config.SplitMode == "none" {
		if err := s.ensureTable(ctx, s.config.Table); err != nil {
			return nil, err
		}
		tables = append(tables, s.config.Table)
	}
	sort.Strings(tables)
	return tables, nil
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
	normalizeRecordTime(&record)
	table := s.tableForTimestamp(record.Timestamp)
	if err := s.ensureTable(ctx, table); err != nil {
		return err
	}
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
	_, err = s.request(ctx, fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT JSONEachRow", s.config.Database, table), append(row, '\n'))
	return err
}

func (s *clickHouseSink) Search(ctx context.Context, query Query) (SearchResult, error) {
	tables, err := s.tablesForSearch(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	if len(tables) == 0 {
		return SearchResult{Records: []Record{}, Page: query.Page, PageSize: query.PageSize}, nil
	}
	selects := make([]string, 0, len(tables))
	for _, table := range tables {
		selects = append(selects, fmt.Sprintf("SELECT timestamp, site_id, record FROM `%s`.`%s`", s.config.Database, table))
	}
	source := "(" + strings.Join(selects, " UNION ALL ") + ") AS access_log_partitions"
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
	countData, err := s.request(ctx, "SELECT count() FROM "+source+" WHERE "+predicate+" FORMAT TabSeparated", nil)
	if err != nil {
		return SearchResult{}, err
	}
	total := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(countData)), "%d", &total); err != nil {
		return SearchResult{}, err
	}
	offset := (query.Page - 1) * query.PageSize
	querySQL := fmt.Sprintf("SELECT record FROM %s WHERE %s ORDER BY timestamp DESC LIMIT %d OFFSET %d FORMAT TabSeparatedRaw", source, predicate, query.PageSize, offset)
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
