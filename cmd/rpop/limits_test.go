package main

import (
	"testing"

	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/overlay"
)

// defaultOptionsForLimits is the zero-configuration set of D31 knobs (as flag.Parse would leave o if every flag
// used its default), so tests only have to spell out the field they are actually varying.
func defaultOptionsForLimits() options {
	return options{
		overlayStreamWindowBytes:     overlay.DefaultStreamWindowBytes,
		overlayConnectionWindowBytes: overlay.DefaultConnectionWindowBytes,
		overlayMaxStreamsPerConn:     overlay.DefaultMaxStreamsPerConn,
		southboundMaxStreamsPerConn:  control.DefaultMaxConcurrentSouthboundStreamsPerConn,
		logIngestMaxConcurrent:       control.DefaultMaxConcurrentLogIngests,
		logIngestRateBytesPerSecond:  control.DefaultLogIngestRateBytesPerSecond,
		tunnelEventStoreMaxBytes:     control.DefaultTunnelEventStoreMaxBytes,
		tunnelEventRetentionDays:     control.DefaultTunnelEventRetentionDays,
		clockSkewWarnThresholdMillis: control.DefaultClockSkewWarnThresholdMillis,
		pathActiveProbe:              true,
	}
}

// TestOverlayConfigDefaultsToTheUnconfiguredOverlay covers D31's zero-configuration guarantee: an options built
// entirely from flag defaults must produce exactly overlay.DefaultConfig(), so a process started with no D31
// flags set behaves exactly as it did before those flags existed.
func TestOverlayConfigDefaultsToTheUnconfiguredOverlay(t *testing.T) {
	cfg, err := defaultOptionsForLimits().overlayConfig()
	if err != nil {
		t.Fatalf("overlayConfig() error = %v", err)
	}
	if cfg != overlay.DefaultConfig() {
		t.Fatalf("overlayConfig() = %+v, want %+v", cfg, overlay.DefaultConfig())
	}
}

// TestOverlayConfigRejectsOutOfRangeValues covers that a bad -overlay-* flag fails at startup with a clear error
// rather than silently misconfiguring every link and relay port the process opens.
func TestOverlayConfigRejectsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*options)
		wantErr bool
	}{
		{"defaults are valid", func(o *options) {}, false},
		{"stream window below the minimum is rejected", func(o *options) { o.overlayStreamWindowBytes = overlay.MinWindowBytes - 1 }, true},
		{"stream window above the maximum is rejected", func(o *options) { o.overlayStreamWindowBytes = overlay.MaxWindowBytes + 1 }, true},
		{"connection window below the minimum is rejected", func(o *options) { o.overlayConnectionWindowBytes = overlay.MinWindowBytes - 1 }, true},
		{"max streams above the limit is rejected", func(o *options) { o.overlayMaxStreamsPerConn = overlay.MaxStreamsPerConnLimit + 1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := defaultOptionsForLimits()
			tt.mutate(&o)
			_, err := o.overlayConfig()
			if (err != nil) != tt.wantErr {
				t.Fatalf("overlayConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateControllerLimits covers every controller-only D31 knob (southbound concurrency, log ingest
// limits, and the tunnel event store's size and retention) rejecting an out-of-range or non-positive value.
func TestValidateControllerLimits(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*options)
		wantErr bool
	}{
		{"defaults are valid", func(o *options) {}, false},
		{"southbound max streams out of range is rejected", func(o *options) { o.southboundMaxStreamsPerConn = control.MaxSouthboundMaxStreamsPerConn + 1 }, true},
		{"log ingest max concurrent must be positive", func(o *options) { o.logIngestMaxConcurrent = 0 }, true},
		{"log ingest rate bytes must be positive", func(o *options) { o.logIngestRateBytesPerSecond = 0 }, true},
		{"tunnel event store max bytes must be positive", func(o *options) { o.tunnelEventStoreMaxBytes = -1 }, true},
		{"tunnel event retention days must be positive", func(o *options) { o.tunnelEventRetentionDays = 0 }, true},
		{"clock skew warn threshold below the minimum is rejected", func(o *options) { o.clockSkewWarnThresholdMillis = control.MinClockSkewWarnThresholdMillis - 1 }, true},
		{"clock skew warn threshold above the maximum is rejected", func(o *options) { o.clockSkewWarnThresholdMillis = control.MaxClockSkewWarnThresholdMillis + 1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := defaultOptionsForLimits()
			tt.mutate(&o)
			err := validateControllerLimits(o)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateControllerLimits() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
