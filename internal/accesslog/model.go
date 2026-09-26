package accesslog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Record struct {
	Timestamp              time.Time           `json:"timestamp"`
	SiteID                 string              `json:"siteId"`
	Method                 string              `json:"method"`
	Path                   string              `json:"path"`
	Protocol               string              `json:"protocol"`
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
	Adapter    string           `json:"adapter"`
	File       FileConfig       `json:"file"`
	ClickHouse ClickHouseConfig `json:"clickhouse"`
	S3         S3Config         `json:"s3"`
}

type FileConfig struct {
	Rotation     string `json:"rotation"`
	MaxSizeBytes int64  `json:"maxSizeBytes"`
	Compress     bool   `json:"compress"`
	KeepFiles    int    `json:"keepFiles"`
}

type ClickHouseConfig struct {
	URL      string `json:"url"`
	Database string `json:"database"`
	Table    string `json:"table"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type S3Config struct {
	Endpoint        string `json:"endpoint,omitempty"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	SessionToken    string `json:"sessionToken,omitempty"`
	ForcePathStyle  bool   `json:"forcePathStyle"`
}

func DefaultConfig() Config {
	return Config{
		Adapter:    "file",
		File:       FileConfig{Rotation: "day", MaxSizeBytes: 1 << 30, Compress: true, KeepFiles: 30},
		ClickHouse: ClickHouseConfig{Database: "default", Table: "access_logs"},
		S3:         S3Config{Region: "us-east-1", Prefix: "rpop/access", ForcePathStyle: true},
	}
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
		parsed, err := url.ParseRequestURI(c.ClickHouse.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("clickhouse.url must be an HTTP(S) URL")
		}
		if !sqlIdentifier.MatchString(c.ClickHouse.Database) || !sqlIdentifier.MatchString(c.ClickHouse.Table) {
			return fmt.Errorf("clickhouse database and table must be SQL identifiers")
		}
	case "s3":
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
		return fmt.Errorf("adapter must be file, clickhouse, or s3")
	}
	return nil
}
