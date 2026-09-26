package accesslog

import (
	"testing"
	"time"
)

// Vectors from the AWS "Signature Version 4 (header-based auth)" examples for Amazon S3.
func TestS3AuthorizationMatchesAWSExamples(t *testing.T) {
	config := S3Config{Region: "us-east-1", AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	tests := []struct {
		name, query, signature string
	}{
		{"get bucket lifecycle", "lifecycle=", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"list objects", "max-keys=2&prefix=J", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s3Authorization(config, "GET", "/", tt.query, "examplebucket.s3.amazonaws.com", emptyPayload, now)
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=" + tt.signature
			if got != want {
				t.Fatalf("authorization mismatch\n got: %s\nwant: %s", got, want)
			}
		})
	}
}
