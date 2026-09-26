package accesslog

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type s3Sink struct {
	config   S3Config
	endpoint *url.URL
	location *time.Location
	client   *http.Client
}

func normalizeS3Config(config S3Config) S3Config {
	defaults := DefaultConfig().S3
	if config.Region == "" {
		config.Region = defaults.Region
	}
	if config.Prefix == "" {
		config.Prefix = defaults.Prefix
	}
	if config.SplitMode == "" {
		config.SplitMode = defaults.SplitMode
	}
	return config
}

func newS3Sink(config S3Config) (*s3Sink, error) {
	config = normalizeS3Config(config)
	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = "https://s3." + config.Region + ".amazonaws.com"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	return &s3Sink{config: config, endpoint: parsed, location: time.UTC, client: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (s *s3Sink) SetTimeZone(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	s.location = location
}

func (s *s3Sink) objectURL(key string, query url.Values) *url.URL {
	u := *s.endpoint
	escapedPath := strings.TrimRight(u.EscapedPath(), "/")
	if s.config.ForcePathStyle {
		escapedPath += "/" + awsEscapePath(s.config.Bucket)
	} else {
		u.Host = s.config.Bucket + "." + u.Host
	}
	if key != "" {
		escapedPath += "/" + awsEscapePath(key)
	} else {
		escapedPath += "/"
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err == nil {
		u.Path = decodedPath
		u.RawPath = escapedPath
	}
	if query != nil {
		u.RawQuery = awsCanonicalQuery(query)
	}
	return &u
}

func awsEscapePath(path string) string {
	const hexDigits = "0123456789ABCDEF"
	parts := strings.Split(path, "/")
	for i, part := range parts {
		var escaped strings.Builder
		for _, b := range []byte(part) {
			if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-_.~", rune(b)) {
				escaped.WriteByte(b)
			} else {
				escaped.WriteByte('%')
				escaped.WriteByte(hexDigits[b>>4])
				escaped.WriteByte(hexDigits[b&15])
			}
		}
		parts[i] = escaped.String()
	}
	return strings.Join(parts, "/")
}

func awsCanonicalQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		vals := append([]string(nil), values[key]...)
		sort.Strings(vals)
		for _, value := range vals {
			parts = append(parts, awsQueryEscape(key)+"="+awsQueryEscape(value))
		}
	}
	return strings.Join(parts, "&")
}
func awsQueryEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(value), "+", "%20"), "%7E", "~")
}

func (s *s3Sink) request(ctx context.Context, method, key string, query url.Values, body []byte) ([]byte, error) {
	u := s.objectURL(key, query)
	payload := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payload[:])
	now := time.Now().UTC()
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-amz-content-sha256", payloadHash)
	request.Header.Set("x-amz-date", now.Format(awsDateTimeFormat))
	if s.config.SessionToken != "" {
		request.Header.Set("x-amz-security-token", s.config.SessionToken)
	}
	request.Header.Set("Authorization", s3Authorization(s.config, method, u.EscapedPath(), awsCanonicalQuery(query), u.Host, payloadHash, now))
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("s3 returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

const awsDateTimeFormat = "20060102T150405Z"

// s3Authorization builds the SigV4 Authorization header for a request whose
// x-amz-date, x-amz-content-sha256 and optional x-amz-security-token headers match now, payloadHash and config.
func s3Authorization(config S3Config, method, escapedPath, canonicalQuery, host, payloadHash string, now time.Time) string {
	now = now.UTC()
	amzDate := now.Format(awsDateTimeFormat)
	shortDate := now.Format("20060102")
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n"
	if config.SessionToken != "" {
		signedHeaders += ";x-amz-security-token"
		canonicalHeaders += "x-amz-security-token:" + config.SessionToken + "\n"
	}
	canonicalRequest := method + "\n" + escapedPath + "\n" + canonicalQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	scope := shortDate + "/" + config.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	kDate := hmacSHA256([]byte("AWS4"+config.SecretAccessKey), shortDate)
	kRegion := hmacSHA256(kDate, config.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	return "AWS4-HMAC-SHA256 Credential=" + config.AccessKeyID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (s *s3Sink) Write(ctx context.Context, record Record) error {
	normalizeRecordTime(&record)
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	randomSuffix := make([]byte, 8)
	if _, err := rand.Read(randomSuffix); err != nil {
		return err
	}
	key := s.objectKey(record, hex.EncodeToString(randomSuffix))
	_, err = s.request(ctx, http.MethodPut, key, nil, compressed.Bytes())
	return err
}

func (s *s3Sink) objectKey(record Record, suffix string) string {
	parts := make([]string, 0, 4)
	if prefix := strings.Trim(s.config.Prefix, "/"); prefix != "" {
		parts = append(parts, prefix)
	}
	stamp := record.Timestamp.In(s.location)
	switch s.config.SplitMode {
	case "day":
		parts = append(parts, stamp.Format("20060102"))
	case "hour":
		parts = append(parts, stamp.Format("20060102"), stamp.Format("15"))
	}
	parts = append(parts, fmt.Sprintf("%d-%s.jsonl.gz", stamp.UnixNano(), suffix))
	return strings.Join(parts, "/")
}

type listObjectsResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func (s *s3Sink) listKeys(ctx context.Context, prefix string) ([]string, error) {
	return s.listKeysWithDelimiter(ctx, prefix, "")
}

func (s *s3Sink) listKeysWithDelimiter(ctx context.Context, prefix, delimiter string) ([]string, error) {
	var keys []string
	token := ""
	for {
		values := url.Values{"list-type": []string{"2"}, "prefix": []string{prefix}}
		if delimiter != "" {
			values.Set("delimiter", delimiter)
		}
		if token != "" {
			values.Set("continuation-token", token)
		}
		data, err := s.request(ctx, http.MethodGet, "", values, nil)
		if err != nil {
			return nil, err
		}
		var result listObjectsResult
		if err := xml.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		for _, item := range result.Contents {
			keys = append(keys, item.Key)
		}
		if !result.IsTruncated {
			break
		}
		if result.NextContinuationToken == "" {
			return nil, fmt.Errorf("s3 list response omitted continuation token")
		}
		token = result.NextContinuationToken
	}
	return keys, nil
}
func (s *s3Sink) rootPrefix() string {
	if prefix := strings.Trim(s.config.Prefix, "/"); prefix != "" {
		return prefix + "/"
	}
	return ""
}

func (s *s3Sink) searchPrefixes(query Query) []string {
	rootPrefix := s.rootPrefix()
	if s.config.SplitMode == "none" || query.From.IsZero() {
		return []string{rootPrefix}
	}
	end := query.To.UTC()
	if query.To.IsZero() {
		end = time.Now().UTC()
	}
	// Partition names do not carry their timezone. Use the system timezone,
	// widened to cover data written before the configured zone changed.
	from := query.From.UTC().Add(-26 * time.Hour).In(s.location)
	end = end.UTC().Add(26 * time.Hour).In(s.location)
	if end.Before(from) {
		return []string{rootPrefix}
	}
	firstDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, s.location)
	lastDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, s.location)
	prefixes := make([]string, 0, 5)
	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		prefixes = append(prefixes, rootPrefix+day.Format("20060102")+"/")
		if len(prefixes) > 5 {
			return []string{rootPrefix}
		}
	}
	return prefixes
}

func objectKeyInTimeRange(key string, query Query) bool {
	filename := key[strings.LastIndex(key, "/")+1:]
	timestampPart, _, ok := strings.Cut(filename, "-")
	if !ok {
		return true
	}
	nanoseconds, err := strconv.ParseInt(timestampPart, 10, 64)
	if err != nil {
		return true
	}
	timestamp := time.Unix(0, nanoseconds).UTC()
	return (query.From.IsZero() || !timestamp.Before(query.From.UTC())) && (query.To.IsZero() || !timestamp.After(query.To.UTC()))
}

func (s *s3Sink) Search(ctx context.Context, query Query) (SearchResult, error) {
	rootPrefix := s.rootPrefix()
	prefixes := s.searchPrefixes(query)
	partitionedSearch := len(prefixes) > 0 && prefixes[0] != rootPrefix
	keys := make([]string, 0)
	seen := make(map[string]struct{})
	for _, prefix := range prefixes {
		prefixKeys, err := s.listKeys(ctx, prefix)
		if err != nil {
			return SearchResult{}, err
		}
		for _, key := range prefixKeys {
			if _, exists := seen[key]; !exists {
				keys = append(keys, key)
				seen[key] = struct{}{}
			}
		}
	}
	if partitionedSearch && (!query.From.IsZero() || !query.To.IsZero()) {
		rootKeys, err := s.listKeysWithDelimiter(ctx, rootPrefix, "/")
		if err != nil {
			return SearchResult{}, err
		}
		for _, key := range rootKeys {
			if _, exists := seen[key]; !exists && objectKeyInTimeRange(key, query) {
				keys = append(keys, key)
				seen[key] = struct{}{}
			}
		}
	}
	var records []Record
	for _, key := range keys {
		if !objectKeyInTimeRange(key, query) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		data, err := s.request(ctx, http.MethodGet, key, nil, nil)
		if err != nil {
			return SearchResult{}, err
		}
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return SearchResult{}, err
		}
		raw, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return SearchResult{}, readErr
		}
		if closeErr != nil {
			return SearchResult{}, closeErr
		}
		var record Record
		if err := json.Unmarshal(raw, &record); err != nil {
			return SearchResult{}, err
		}
		if matches(record, query) {
			records = append(records, record)
		}
	}
	return paginate(records, query), nil
}
func (s *s3Sink) Close() error { return nil }
