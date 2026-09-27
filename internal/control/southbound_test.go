package control

import "testing"

// TestSouthboundHTTP2ConfigBoundsConcurrentStreams covers stage 5 security review item 5: the southbound
// listener's HTTP/2 tuning must cap concurrent streams per connection, mirroring the protection
// internal/overlay's relay port already applies to node-to-node links.
func TestSouthboundHTTP2ConfigBoundsConcurrentStreams(t *testing.T) {
	cfg := SouthboundHTTP2Config()
	if cfg.MaxConcurrentStreams != MaxConcurrentSouthboundStreamsPerConn {
		t.Fatalf("MaxConcurrentStreams = %d, want %d", cfg.MaxConcurrentStreams, MaxConcurrentSouthboundStreamsPerConn)
	}
}
