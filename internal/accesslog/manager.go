package accesslog

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

type Sink interface {
	Write(context.Context, Record) error
	Search(context.Context, Query) (SearchResult, error)
	Close() error
}

type timeZoneAwareSink interface {
	SetTimeZone(*time.Location)
}

type Manager struct {
	mu       sync.RWMutex
	dir      string
	config   Config
	sink     Sink
	location *time.Location
}

func NewManager(dir string, config Config) (*Manager, error) {
	config = normalizeConfig(config)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	sink, err := openSink(dir, config)
	if err != nil {
		return nil, err
	}
	return &Manager{dir: dir, config: config, sink: sink, location: time.UTC}, nil
}

func normalizeConfig(c Config) Config {
	defaults := DefaultConfig()
	if c.Adapter == "" {
		c.Adapter = defaults.Adapter
	}
	if c.File.Rotation == "" {
		c.File.Rotation = defaults.File.Rotation
	}
	if c.File.MaxSizeBytes == 0 {
		c.File.MaxSizeBytes = defaults.File.MaxSizeBytes
	}
	if c.ClickHouse.Database == "" {
		c.ClickHouse.Database = defaults.ClickHouse.Database
	}
	if c.ClickHouse.Table == "" {
		c.ClickHouse.Table = defaults.ClickHouse.Table
	}
	if c.ClickHouse.SplitMode == "" {
		c.ClickHouse.SplitMode = defaults.ClickHouse.SplitMode
	}
	if c.S3.Region == "" {
		c.S3.Region = defaults.S3.Region
	}
	if c.S3.Prefix == "" {
		c.S3.Prefix = defaults.S3.Prefix
	}
	if c.S3.SplitMode == "" {
		c.S3.SplitMode = defaults.S3.SplitMode
	}
	c.Elasticsearch = normalizeElasticsearchConfig(c.Elasticsearch)
	return c
}

func normalizeElasticsearchConfig(config ElasticsearchConfig) ElasticsearchConfig {
	defaults := DefaultConfig().Elasticsearch
	if config.Index == "" {
		config.Index = defaults.Index
	}
	if config.SplitMode == "" {
		config.SplitMode = defaults.SplitMode
	}
	if config.AuthType == "" {
		switch {
		case config.APIKey != "":
			config.AuthType = "apiKey"
		case config.Username != "" || config.Password != "":
			config.AuthType = "basic"
		default:
			config.AuthType = defaults.AuthType
		}
	}
	return config
}

func openSink(dir string, c Config) (Sink, error) {
	switch c.Adapter {
	case "file":
		return newFileSink(dir, c.File)
	case "clickhouse":
		return newClickHouseSink(c.ClickHouse)
	case "s3":
		return newS3Sink(c.S3)
	case "elasticsearch":
		return newElasticsearchSink(c.Elasticsearch)
	default:
		return nil, c.Validate()
	}
}

func (m *Manager) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

func setSinkTimeZone(sink Sink, location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	if aware, ok := sink.(timeZoneAwareSink); ok {
		aware.SetTimeZone(location)
	}
}

func (m *Manager) SetTimeZone(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.location = location
	setSinkTimeZone(m.sink, location)
}

// Configure opens and validates the replacement sink before persisting and swapping it.
func (m *Manager) Configure(ctx context.Context, config Config, persist func([]byte) error) error {
	config = normalizeConfig(config)
	if err := config.Validate(); err != nil {
		return err
	}
	next, err := openSink(m.dir, config)
	if err != nil {
		return err
	}
	serialized, err := json.Marshal(config)
	if err != nil {
		_ = next.Close()
		return err
	}
	m.mu.Lock()
	setSinkTimeZone(next, m.location)
	if persist != nil {
		if err := persist(serialized); err != nil {
			m.mu.Unlock()
			_ = next.Close()
			return err
		}
	}
	previous := m.sink
	m.sink = next
	m.config = config
	m.mu.Unlock()
	if previous != nil {
		return previous.Close()
	}
	return nil
}

func (m *Manager) Write(ctx context.Context, record Record) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sink.Write(ctx, record)
}

func (m *Manager) Search(ctx context.Context, query Query) (SearchResult, error) {
	query = normalizeQuery(query)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sink.Search(ctx, query)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sink == nil {
		return nil
	}
	err := m.sink.Close()
	m.sink = nil
	return err
}

func normalizeQuery(q Query) Query {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = 25
	}
	if q.PageSize > 100 {
		q.PageSize = 100
	}
	q.Text = strings.TrimSpace(q.Text)
	q.SiteID = strings.TrimSpace(q.SiteID)
	return q
}

func matches(record Record, query Query) bool {
	if query.SiteID != "" && record.SiteID != query.SiteID {
		return false
	}
	if query.Status > 0 && record.Status != query.Status {
		return false
	}
	if !query.From.IsZero() && record.Timestamp.Before(query.From) {
		return false
	}
	if !query.To.IsZero() && record.Timestamp.After(query.To) {
		return false
	}
	if query.Text != "" {
		encoded, _ := json.Marshal(record)
		if !strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(query.Text)) {
			return false
		}
	}
	return true
}

func paginate(records []Record, query Query) SearchResult {
	sort.Slice(records, func(i, j int) bool { return records[i].Timestamp.After(records[j].Timestamp) })
	total := len(records)
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	page := append([]Record(nil), records[start:end]...)
	if page == nil {
		page = []Record{}
	}
	return SearchResult{Records: page, Total: total, Page: query.Page, PageSize: query.PageSize}
}

func normalizeRecordTime(record *Record) {
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	} else {
		record.Timestamp = record.Timestamp.UTC()
	}
}
