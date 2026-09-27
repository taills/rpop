package accesslog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Record struct {
	Timestamp time.Time `json:"timestamp"`
	SiteID    string    `json:"siteId"`
	// TrackID is the Rpop-Track-Id the ingress node minted for this request (D22); TunnelID names the cross-node
	// tunnel the request's upstream connection used, empty for direct (single-node) upstreams.
	TrackID                string              `json:"trackId,omitempty"`
	TunnelID               string              `json:"tunnelId,omitempty"`
	ClientIP               string              `json:"clientIp,omitempty"`
	ClientPort             int                 `json:"clientPort,omitempty"`
	ForwardedFor           string              `json:"forwardedFor,omitempty"`
	Scheme                 string              `json:"scheme,omitempty"`
	TLSVersion             string              `json:"tlsVersion,omitempty"`
	Host                   string              `json:"host,omitempty"`
	Method                 string              `json:"method"`
	Path                   string              `json:"path"`
	Protocol               string              `json:"protocol"`
	Referer                string              `json:"referer,omitempty"`
	UserAgent              string              `json:"userAgent,omitempty"`
	Upstream               string              `json:"upstream,omitempty"`
	Route                  string              `json:"route,omitempty"`
	RequestHeaders         map[string][]string `json:"requestHeaders"`
	Status                 int                 `json:"status"`
	ResponseHeaders        map[string][]string `json:"responseHeaders"`
	RequestBody            string              `json:"requestBody,omitempty"`
	RequestBodyEncoding    string              `json:"requestBodyEncoding,omitempty"`
	RequestBodyTotalBytes  int64               `json:"requestBodyTotalBytes,omitempty"`
	RequestBodyTruncated   bool                `json:"requestBodyTruncated,omitempty"`
	ResponseBody           string              `json:"responseBody,omitempty"`
	ResponseBodyEncoding   string              `json:"responseBodyEncoding,omitempty"`
	ResponseBodyTotalBytes int64               `json:"responseBodyTotalBytes,omitempty"`
	ResponseBodyTruncated  bool                `json:"responseBodyTruncated,omitempty"`
	RequestBytes           uint64              `json:"requestBytes"`
	ResponseBytes          uint64              `json:"responseBytes"`
	TTFBMillis             float64             `json:"ttfbMillis"`
	ResponseMillis         float64             `json:"responseMillis"`
}

type Query struct {
	Text     string
	SiteID   string
	Status   int
	// TrackID filters to the one record (if any) carrying this Rpop-Track-Id (D22); see GET
	// /api/logging/trace/{trackId} in internal/control.
	TrackID  string
	From     time.Time
	To       time.Time
	Page     int
	PageSize int
}

type SearchResult struct {
	Records  []Record `json:"records"`
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
}

type Config struct {
	Adapter       string              `json:"adapter"`
	File          FileConfig          `json:"file"`
	ClickHouse    ClickHouseConfig    `json:"clickhouse"`
	S3            S3Config            `json:"s3"`
	Elasticsearch ElasticsearchConfig `json:"elasticsearch"`
}

type FileConfig struct {
	Rotation     string `json:"rotation"`
	MaxSizeBytes int64  `json:"maxSizeBytes"`
	Compress     bool   `json:"compress"`
	KeepFiles    int    `json:"keepFiles"`
}

type ClickHouseConfig struct {
	URL       string `json:"url"`
	Database  string `json:"database"`
	SplitMode string `json:"splitMode"`
	Table     string `json:"table"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
}

type S3Config struct {
	Endpoint        string `json:"endpoint,omitempty"`
	Region          string `json:"region"`
	SplitMode       string `json:"splitMode"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	SessionToken    string `json:"sessionToken,omitempty"`
	ForcePathStyle  bool   `json:"forcePathStyle"`
}

type ElasticsearchConfig struct {
	URL       string `json:"url"`
	Index     string `json:"index"`
	SplitMode string `json:"splitMode"`
	AuthType  string `json:"authType"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	APIKey    string `json:"apiKey,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Adapter:       "file",
		File:          FileConfig{Rotation: "day", MaxSizeBytes: 1 << 30, Compress: true, KeepFiles: 30},
		ClickHouse:    ClickHouseConfig{Database: "default", Table: "access_logs", SplitMode: "none"},
		S3:            S3Config{Region: "us-east-1", Prefix: "rpop/access", SplitMode: "hour", ForcePathStyle: true},
		Elasticsearch: ElasticsearchConfig{Index: "rpop-access-logs", SplitMode: "none", AuthType: "none"},
	}
}

var (
	sqlIdentifier             = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	elasticsearchIndexPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)
)

func validPartitionMode(mode string) bool {
	return mode == "none" || mode == "day" || mode == "hour"
}

func (c Config) Validate() error {
	switch c.Adapter {
	case "file":
		if c.File.Rotation == "" {
			c.File.Rotation = "day"
		}
		if c.File.Rotation != "day" && c.File.Rotation != "hour" && c.File.Rotation != "size" {
			return fmt.Errorf("file.rotation must be day, hour, or size")
		}
		if c.File.MaxSizeBytes <= 0 {
			return fmt.Errorf("file.maxSizeBytes must be positive")
		}
		if c.File.KeepFiles < 0 || c.File.KeepFiles > 10000 {
			return fmt.Errorf("file.keepFiles must be between 0 and 10000")
		}
	case "clickhouse":
		if c.ClickHouse.SplitMode != "" && !validPartitionMode(c.ClickHouse.SplitMode) {
			return fmt.Errorf("clickhouse.splitMode must be none, day, or hour")
		}
		parsed, err := url.ParseRequestURI(c.ClickHouse.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("clickhouse.url must be an HTTP(S) URL")
		}
		if !sqlIdentifier.MatchString(c.ClickHouse.Database) || !sqlIdentifier.MatchString(c.ClickHouse.Table) {
			return fmt.Errorf("clickhouse database and table must be SQL identifiers")
		}
	case "elasticsearch":
		if c.Elasticsearch.SplitMode != "" && !validPartitionMode(c.Elasticsearch.SplitMode) {
			return fmt.Errorf("elasticsearch.splitMode must be none, day, or hour")
		}
		parsed, err := url.ParseRequestURI(c.Elasticsearch.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("elasticsearch.url must be an HTTP(S) URL without credentials, query, or fragment")
		}
		if !elasticsearchIndexPattern.MatchString(c.Elasticsearch.Index) || c.Elasticsearch.Index == "." || c.Elasticsearch.Index == ".." {
			return fmt.Errorf("elasticsearch.index must be a lowercase index name containing only letters, numbers, dots, hyphens, or underscores")
		}
		suffixLength := 0
		switch c.Elasticsearch.SplitMode {
		case "day":
			suffixLength = 9
		case "hour":
			suffixLength = 11
		}
		if len(c.Elasticsearch.Index)+suffixLength > 255 {
			return fmt.Errorf("elasticsearch.index is too long for the selected splitMode")
		}
		switch c.Elasticsearch.AuthType {
		case "none":
			if c.Elasticsearch.Username != "" || c.Elasticsearch.Password != "" || c.Elasticsearch.APIKey != "" {
				return fmt.Errorf("elasticsearch credentials must be empty when authType is none")
			}
		case "basic":
			if strings.TrimSpace(c.Elasticsearch.Username) == "" || c.Elasticsearch.Password == "" || c.Elasticsearch.APIKey != "" {
				return fmt.Errorf("elasticsearch basic auth requires a username and password and cannot include an API key")
			}
		case "apiKey":
			if strings.TrimSpace(c.Elasticsearch.APIKey) == "" || c.Elasticsearch.Username != "" || c.Elasticsearch.Password != "" {
				return fmt.Errorf("elasticsearch apiKey auth requires an API key and cannot include basic credentials")
			}
		default:
			return fmt.Errorf("elasticsearch.authType must be none, basic, or apiKey")
		}
	case "s3":
		if c.S3.SplitMode != "" && !validPartitionMode(c.S3.SplitMode) {
			return fmt.Errorf("s3.splitMode must be none, day, or hour")
		}
		if strings.TrimSpace(c.S3.Bucket) == "" {
			return fmt.Errorf("s3.bucket is required")
		}
		if c.S3.Region == "" {
			return fmt.Errorf("s3.region is required")
		}
		if c.S3.AccessKeyID == "" || c.S3.SecretAccessKey == "" {
			return fmt.Errorf("s3 access key and secret key are required")
		}
		if c.S3.Endpoint != "" {
			parsed, err := url.ParseRequestURI(c.S3.Endpoint)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("s3.endpoint must be an HTTP(S) URL")
			}
		}
	default:
		return fmt.Errorf("adapter must be file, clickhouse, elasticsearch, or s3")
	}
	return nil
}
