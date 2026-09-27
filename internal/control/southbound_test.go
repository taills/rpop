package control

import "testing"

// TestSouthboundHTTP2ConfigBoundsConcurrentStreams covers stage 5 security review item 5: the southbound
// listener's HTTP/2 tuning must cap concurrent streams per connection, mirroring the protection
// internal/overlay's relay port already applies to node-to-node links.
func TestSouthboundHTTP2ConfigBoundsConcurrentStreams(t *testing.T) {
	cfg := SouthboundHTTP2Config(DefaultMaxConcurrentSouthboundStreamsPerConn)
	if cfg.MaxConcurrentStreams != DefaultMaxConcurrentSouthboundStreamsPerConn {
		t.Fatalf("MaxConcurrentStreams = %d, want %d", cfg.MaxConcurrentStreams, DefaultMaxConcurrentSouthboundStreamsPerConn)
	}
}

// TestValidateSouthboundMaxStreamsPerConn covers the D31 bounds a configured value must satisfy.
func TestValidateSouthboundMaxStreamsPerConn(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"default is valid", DefaultMaxConcurrentSouthboundStreamsPerConn, false},
		{"minimum is valid", MinSouthboundMaxStreamsPerConn, false},
		{"maximum is valid", MaxSouthboundMaxStreamsPerConn, false},
		{"below minimum is rejected", MinSouthboundMaxStreamsPerConn - 1, true},
		{"above maximum is rejected", MaxSouthboundMaxStreamsPerConn + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSouthboundMaxStreamsPerConn(tt.n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSouthboundMaxStreamsPerConn(%d) error = %v, wantErr %v", tt.n, err, tt.wantErr)
			}
		})
	}
}
