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
	"strings"
	"time"
)

type s3Sink struct {
	config   S3Config
	endpoint *url.URL
	client   *http.Client
}

func newS3Sink(config S3Config) (*s3Sink, error) {
	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = "https://s3." + config.Region + ".amazonaws.com"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	return &s3Sink{config: config, endpoint: parsed, client: &http.Client{Timeout: 60 * time.Second}}, nil
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
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-amz-content-sha256", payloadHash)
	request.Header.Set("x-amz-date", amzDate)
	if s.config.SessionToken != "" {
		request.Header.Set("x-amz-security-token", s.config.SessionToken)
	}
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + u.Host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n"
	if s.config.SessionToken != "" {
		signedHeaders += ";x-amz-security-token"
		canonicalHeaders += "x-amz-security-token:" + s.config.SessionToken + "\n"
	}
	canonicalRequest := method + "\n" + u.EscapedPath() + "\n" + awsCanonicalQuery(query) + "\n" + canonicalHeaders + signedHeaders + "\n" + payloadHash
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	scope := shortDate + "/" + s.config.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	kDate := hmacSHA256([]byte("AWS4"+s.config.SecretAccessKey), shortDate)
	kRegion := hmacSHA256(kDate, s.config.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.config.AccessKeyID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
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
	prefix := strings.Trim(s.config.Prefix, "/")
	key := fmt.Sprintf("%s/%s/%s/%d-%s.jsonl.gz", prefix, record.Timestamp.UTC().Format("20060102"), record.Timestamp.UTC().Format("15"), record.Timestamp.UTC().UnixNano(), hex.EncodeToString(randomSuffix))
	_, err = s.request(ctx, http.MethodPut, key, nil, compressed.Bytes())
	return err
}

type listObjectsResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func (s *s3Sink) listKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		values := url.Values{"list-type": []string{"2"}, "prefix": []string{prefix}}
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
func (s *s3Sink) Search(ctx context.Context, query Query) (SearchResult, error) {
	prefix := strings.Trim(s.config.Prefix, "/") + "/"
	if !query.From.IsZero() {
		end := query.To
		if end.IsZero() {
			end = time.Now().UTC()
		}
		if query.From.UTC().Format("20060102") == end.UTC().Format("20060102") {
			prefix += query.From.UTC().Format("20060102") + "/"
		}
	}
	keys, err := s.listKeys(ctx, prefix)
	if err != nil {
		return SearchResult{}, err
	}
	var records []Record
	for _, key := range keys {
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
