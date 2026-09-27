package main

import (
	"fmt"

	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/overlay"
)

// overlayConfig builds the overlay.Config every node, and the embedded node inside a controller, is built with
// (D31): validated once here so a bad -overlay-* flag or environment variable fails at startup with a clear
// message instead of silently misconfiguring every link and relay port the process opens.
func (o options) overlayConfig() (overlay.Config, error) {
	cfg := overlay.Config{
		StreamWindowBytes: o.overlayStreamWindowBytes, ConnectionWindowBytes: o.overlayConnectionWindowBytes,
		MaxStreamsPerConn: o.overlayMaxStreamsPerConn,
	}
	if err := overlay.ValidateConfig(cfg); err != nil {
		return overlay.Config{}, err
	}
	return cfg, nil
}

// validateControllerLimits checks every controller-only D31 knob (southbound concurrency, log ingest limits, and
// the tunnel event store's size and retention) in one pass, so a bad flag is caught before the controller starts
// serving traffic rather than only the first one flag.Parse happens to reach.
func validateControllerLimits(o options) error {
	if err := control.ValidateSouthboundMaxStreamsPerConn(o.southboundMaxStreamsPerConn); err != nil {
		return err
	}
	if o.logIngestMaxConcurrent < 1 {
		return fmt.Errorf("-log-ingest-max-concurrent must be at least 1, got %d", o.logIngestMaxConcurrent)
	}
	if o.logIngestRateBytesPerSecond < 1 {
		return fmt.Errorf("-log-ingest-rate-bytes must be at least 1, got %d", o.logIngestRateBytesPerSecond)
	}
	if o.tunnelEventStoreMaxBytes < 1 {
		return fmt.Errorf("-tunnel-event-store-max-bytes must be at least 1, got %d", o.tunnelEventStoreMaxBytes)
	}
	if o.tunnelEventRetentionDays < 1 {
		return fmt.Errorf("-tunnel-event-retention-days must be at least 1, got %d", o.tunnelEventRetentionDays)
	}
	return nil
}
