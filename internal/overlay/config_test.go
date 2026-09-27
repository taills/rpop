package overlay

import (
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
)

// TestDefaultConfigMatchesThePreD31Constants pins DefaultConfig to the exact values the hard-coded constants it
// replaces used to have (D31): a zero-configuration process must behave exactly as before.
func TestDefaultConfigMatchesThePreD31Constants(t *testing.T) {
	got := DefaultConfig()
	want := Config{StreamWindowBytes: 16 << 20, ConnectionWindowBytes: 64 << 20, MaxStreamsPerConn: 1000}
	if got != want {
		t.Fatalf("DefaultConfig() = %+v, want %+v", got, want)
	}
}

// TestValidateConfig covers the bounds a Config must satisfy (D31): CLI flags and environment variables must be
// rejected at startup, with a clear message, rather than silently misconfigure every link and relay port.
func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"defaults are valid", DefaultConfig(), false},
		{"minimum window and stream bounds are valid", Config{StreamWindowBytes: MinWindowBytes, ConnectionWindowBytes: MinWindowBytes, MaxStreamsPerConn: MinStreamsPerConn}, false},
		{"maximum window and stream bounds are valid", Config{StreamWindowBytes: MaxWindowBytes, ConnectionWindowBytes: MaxWindowBytes, MaxStreamsPerConn: MaxStreamsPerConnLimit}, false},
		{"stream window below the minimum is rejected", Config{StreamWindowBytes: MinWindowBytes - 1, ConnectionWindowBytes: MinWindowBytes, MaxStreamsPerConn: MinStreamsPerConn}, true},
		{"stream window above the maximum is rejected", Config{StreamWindowBytes: MaxWindowBytes + 1, ConnectionWindowBytes: MinWindowBytes, MaxStreamsPerConn: MinStreamsPerConn}, true},
		{"connection window below the minimum is rejected", Config{StreamWindowBytes: MinWindowBytes, ConnectionWindowBytes: MinWindowBytes - 1, MaxStreamsPerConn: MinStreamsPerConn}, true},
		{"connection window above the maximum is rejected", Config{StreamWindowBytes: MinWindowBytes, ConnectionWindowBytes: MaxWindowBytes + 1, MaxStreamsPerConn: MinStreamsPerConn}, true},
		{"max streams below the minimum is rejected", Config{StreamWindowBytes: MinWindowBytes, ConnectionWindowBytes: MinWindowBytes, MaxStreamsPerConn: MinStreamsPerConn - 1}, true},
		{"max streams above the limit is rejected", Config{StreamWindowBytes: MinWindowBytes, ConnectionWindowBytes: MinWindowBytes, MaxStreamsPerConn: MaxStreamsPerConnLimit + 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateConfig(%+v) error = %v, wantErr %v", tt.cfg, err, tt.wantErr)
			}
		})
	}
}

// TestNewLinkAppliesTheConfiguredWindows covers a link's client transport actually using a custom Config (D31)
// instead of the values DefaultConfig would have set, so an operator's flag really changes the HTTP/2 window a
// link dials with.
func TestNewLinkAppliesTheConfiguredWindows(t *testing.T) {
	ca, err := pki.NewCA("link config test CA")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{StreamWindowBytes: 128 << 10, ConnectionWindowBytes: 256 << 10, MaxStreamsPerConn: 7}
	l := newLink(identityFor(t, ca, "client", 1), "peer", "127.0.0.1:1", nil,
		func() (int64, bool) { return 1, true }, cfg, zap.NewNop())
	defer l.retire()
	if got := l.transport.HTTP2.MaxReceiveBufferPerStream; got != cfg.StreamWindowBytes {
		t.Fatalf("MaxReceiveBufferPerStream = %d, want %d", got, cfg.StreamWindowBytes)
	}
	if got := l.transport.HTTP2.MaxReceiveBufferPerConnection; got != cfg.ConnectionWindowBytes {
		t.Fatalf("MaxReceiveBufferPerConnection = %d, want %d", got, cfg.ConnectionWindowBytes)
	}
}

// TestStartRelayAppliesTheConfiguredWindowsAndStreamLimit covers the relay port's server-side HTTP/2 tuning
// actually using a custom Config (D31), mirroring TestNewLinkAppliesTheConfiguredWindows for the accepting side.
func TestStartRelayAppliesTheConfiguredWindowsAndStreamLimit(t *testing.T) {
	ca, err := pki.NewCA("relay config test CA")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{StreamWindowBytes: 128 << 10, ConnectionWindowBytes: 256 << 10, MaxStreamsPerConn: 7}
	o := New(identityFor(t, ca, "node1", 1), zap.NewNop(), cfg)
	relay, err := o.startRelay(freeAddress(t))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.close()
	if got := relay.server.HTTP2.MaxConcurrentStreams; got != cfg.MaxStreamsPerConn {
		t.Fatalf("MaxConcurrentStreams = %d, want %d", got, cfg.MaxStreamsPerConn)
	}
	if got := relay.server.HTTP2.MaxReceiveBufferPerStream; got != cfg.StreamWindowBytes {
		t.Fatalf("MaxReceiveBufferPerStream = %d, want %d", got, cfg.StreamWindowBytes)
	}
	if got := relay.server.HTTP2.MaxReceiveBufferPerConnection; got != cfg.ConnectionWindowBytes {
		t.Fatalf("MaxReceiveBufferPerConnection = %d, want %d", got, cfg.ConnectionWindowBytes)
	}
}
