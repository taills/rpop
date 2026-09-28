package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/agent"
	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/spool"
	"github.com/rpop-project/rpop/internal/store"
	"github.com/rpop-project/rpop/web"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

type options struct {
	mode, serverAddr, southboundAddr, webDir, dbPath, logDir string
	controllerURL, joinToken, dataDir, relayListen           string
	logSpoolQuotaBytes, logUploadRateBytes                   int64
	// consoleHostnames is the raw, comma-separated -console-hostnames flag; see parseConsoleHostnames.
	consoleHostnames string
	// D31: overlay HTTP/2 window and stream limits, shared by node mode and the controller's embedded node.
	overlayStreamWindowBytes, overlayConnectionWindowBytes, overlayMaxStreamsPerConn int
	// D31: controller-only tuning (southbound concurrency, log ingest limits, tunnel event store).
	southboundMaxStreamsPerConn                           int
	logIngestMaxConcurrent                                int
	logIngestRateBytesPerSecond, tunnelEventStoreMaxBytes int64
	tunnelEventRetentionDays                              int
	// clockSkewWarnThresholdMillis is the controller-only D28 knob for Control.SetClockSkewWarnThreshold.
	clockSkewWarnThresholdMillis int64
	// D30: node mode and the controller's embedded node share this active-probe switch.
	pathActiveProbe bool
}

func main() {
	var o options
	flag.StringVar(&o.mode, "mode", envDefault("RPOP_MODE", modeAllInOne), "all-in-one, controller, or node (env RPOP_MODE)")
	flag.StringVar(&o.serverAddr, "addr", envDefault("RPOP_ADDR", "127.0.0.1:8080"), "control API listen address (env RPOP_ADDR)")
	flag.StringVar(&o.southboundAddr, "southbound-addr", envDefault("RPOP_SOUTHBOUND_ADDR", ""), "address nodes connect to; default :7443 in controller mode, off in all-in-one mode (env RPOP_SOUTHBOUND_ADDR)")
	flag.StringVar(&o.webDir, "web-dir", envDefault("RPOP_WEB_DIR", ""), "serve the frontend from this directory instead of the embedded build (env RPOP_WEB_DIR)")
	flag.StringVar(&o.dbPath, "db", envDefault("RPOP_DB", "data/rpop.db"), "SQLite database path (env RPOP_DB)")
	flag.StringVar(&o.logDir, "log-dir", envDefault("RPOP_LOG_DIR", "logs"), "directory for application logs (env RPOP_LOG_DIR)")
	flag.StringVar(&o.controllerURL, "controller", envDefault("RPOP_CONTROLLER", ""), "node mode: controller southbound URL, e.g. https://controller:7443 (env RPOP_CONTROLLER)")
	flag.StringVar(&o.joinToken, "join-token", envDefault("RPOP_JOIN_TOKEN", ""), "node mode: join token for the first registration (env RPOP_JOIN_TOKEN)")
	flag.StringVar(&o.dataDir, "data-dir", envDefault("RPOP_DATA_DIR", "data/node"), "node mode: directory for the node identity and snapshot cache (env RPOP_DATA_DIR)")
	flag.StringVar(&o.relayListen, "relay-listen", envDefault("RPOP_RELAY_LISTEN", ""), "node mode: bind the relay port here instead of on the port of the node's relay address (env RPOP_RELAY_LISTEN)")
	flag.StringVar(&o.consoleHostnames, "console-hostnames", envDefault("RPOP_CONSOLE_HOSTNAMES", ""), "comma-separated hostnames the control console answers on when -addr is shared with a site; unset means it answers every Host no site claims, as before this flag existed (env RPOP_CONSOLE_HOSTNAMES)")
	flag.Int64Var(&o.logSpoolQuotaBytes, "log-spool-quota-bytes", envDefaultInt64("RPOP_LOG_SPOOL_QUOTA_BYTES", spool.DefaultQuotaBytes), "node mode: disk quota for the log spool awaiting upload (env RPOP_LOG_SPOOL_QUOTA_BYTES)")
	flag.Int64Var(&o.logUploadRateBytes, "log-upload-rate-bytes", envDefaultInt64("RPOP_LOG_UPLOAD_RATE_BYTES", spool.DefaultUploadRateBytesPerSecond), "node mode: max bytes/second spent uploading spooled logs to the controller (env RPOP_LOG_UPLOAD_RATE_BYTES)")
	flag.IntVar(&o.overlayStreamWindowBytes, "overlay-stream-window", envDefaultInt("RPOP_OVERLAY_STREAM_WINDOW", overlay.DefaultStreamWindowBytes), "HTTP/2 per-stream flow-control window for overlay links and the relay port, in bytes (env RPOP_OVERLAY_STREAM_WINDOW)")
	flag.IntVar(&o.overlayConnectionWindowBytes, "overlay-connection-window", envDefaultInt("RPOP_OVERLAY_CONNECTION_WINDOW", overlay.DefaultConnectionWindowBytes), "HTTP/2 per-connection flow-control window for overlay links and the relay port, in bytes (env RPOP_OVERLAY_CONNECTION_WINDOW)")
	flag.IntVar(&o.overlayMaxStreamsPerConn, "overlay-max-streams", envDefaultInt("RPOP_OVERLAY_MAX_STREAMS", overlay.DefaultMaxStreamsPerConn), "max concurrent tunnels the relay port accepts on one overlay connection (env RPOP_OVERLAY_MAX_STREAMS)")
	flag.IntVar(&o.southboundMaxStreamsPerConn, "southbound-max-streams", envDefaultInt("RPOP_SOUTHBOUND_MAX_STREAMS", control.DefaultMaxConcurrentSouthboundStreamsPerConn), "controller mode: max concurrent HTTP/2 streams the southbound listener accepts on one connection (env RPOP_SOUTHBOUND_MAX_STREAMS)")
	flag.IntVar(&o.logIngestMaxConcurrent, "log-ingest-max-concurrent", envDefaultInt("RPOP_LOG_INGEST_MAX_CONCURRENT", control.DefaultMaxConcurrentLogIngests), "controller mode: max log segment uploads processed at once, across every node (env RPOP_LOG_INGEST_MAX_CONCURRENT)")
	flag.Int64Var(&o.logIngestRateBytesPerSecond, "log-ingest-rate-bytes", envDefaultInt64("RPOP_LOG_INGEST_RATE_BYTES", control.DefaultLogIngestRateBytesPerSecond), "controller mode: max bytes/second of compressed log segments accepted from a single node (env RPOP_LOG_INGEST_RATE_BYTES)")
	flag.Int64Var(&o.tunnelEventStoreMaxBytes, "tunnel-event-store-max-bytes", envDefaultInt64("RPOP_TUNNEL_EVENT_STORE_MAX_BYTES", control.DefaultTunnelEventStoreMaxBytes), "controller mode: disk quota for the tunnel event store (env RPOP_TUNNEL_EVENT_STORE_MAX_BYTES)")
	flag.IntVar(&o.tunnelEventRetentionDays, "tunnel-event-retention-days", envDefaultInt("RPOP_TUNNEL_EVENT_RETENTION_DAYS", control.DefaultTunnelEventRetentionDays), "controller mode: days tunnel events stay queryable before pruning (env RPOP_TUNNEL_EVENT_RETENTION_DAYS)")
	flag.Int64Var(&o.clockSkewWarnThresholdMillis, "clock-skew-warn-threshold", envDefaultInt64("RPOP_CLOCK_SKEW_WARN_THRESHOLD", control.DefaultClockSkewWarnThresholdMillis), "controller mode: node clock skew (D28), in milliseconds, above which the console marks a node's clockSkewStatus \"warn\" (env RPOP_CLOCK_SKEW_WARN_THRESHOLD)")
	flag.BoolVar(&o.pathActiveProbe, "path-active-probe", envDefaultBool("RPOP_PATH_ACTIVE_PROBE", true), "enable D19 active probing of a candidate path as soon as its cooldown ends, for node mode and the controller's embedded node alike (env RPOP_PATH_ACTIVE_PROBE)")
	healthCheck := flag.Bool("health-check", false, "probe the control API at -addr and exit 0 when healthy (for container health checks)")
	flag.Parse()

	if *healthCheck {
		// Node mode runs no control API listener to probe (see runNode); agent.Healthy checks the node's local
		// identity instead. See its doc comment for why that is a meaningful signal here.
		if o.mode == modeNode {
			if err := agent.Healthy(o.dataDir); err != nil {
				log.Fatal(err)
			}
			return
		}
		hostHeader := ""
		names, err := parseConsoleHostnames(o.consoleHostnames)
		if err != nil {
			log.Fatal(err)
		}
		if len(names) > 0 {
			hostHeader = names[0]
		}
		if err := runHealthCheck(o.serverAddr, hostHeader); err != nil {
			log.Fatal(err)
		}
		return
	}
	overlayCfg, err := o.overlayConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := validateControllerLimits(o); err != nil {
		log.Fatal(err)
	}
	southbound, err := southboundAddress(o.mode, o.southboundAddr)
	if err != nil {
		log.Fatal(err)
	}
	o.southboundAddr = southbound

	if err := os.MkdirAll(o.logDir, 0750); err != nil {
		log.Fatal(err)
	}
	loggerConfig := zap.NewProductionConfig()
	// Mirror the diagnostic log to stderr so container runtimes (docker logs) show it.
	loggerConfig.OutputPaths = []string{"stderr", filepath.Join(o.logDir, "rpop.log")}
	logger, err := loggerConfig.Build()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(o.logDir, "rpop.log"), 0640); err != nil {
		logger.Fatal("set log permissions", zap.Error(err))
	}
	defer logger.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.mode == modeNode {
		runNode(ctx, logger, o, overlayCfg)
		return
	}
	runController(ctx, logger, o, overlayCfg)
}

func runNode(ctx context.Context, logger *zap.Logger, o options, overlayCfg overlay.Config) {
	node, err := agent.New(agent.Config{
		ControllerURL: o.controllerURL, JoinToken: o.joinToken, DataDir: o.dataDir, RelayListen: o.relayListen, Version: Version,
		LogDir: o.logDir, LogSpoolQuotaBytes: o.logSpoolQuotaBytes, LogUploadRateBytesPerSecond: o.logUploadRateBytes,
		OverlayConfig: overlayCfg, PathActiveProbe: &o.pathActiveProbe,
	}, logger)
	if err != nil {
		logger.Fatal("configure node", zap.Error(err))
	}
	if err := node.Run(ctx); err != nil {
		logger.Fatal("run node", zap.Error(err))
	}
}

func runController(ctx context.Context, logger *zap.Logger, o options, overlayCfg overlay.Config) {
	if err := os.MkdirAll(filepath.Dir(o.dbPath), 0700); err != nil {
		logger.Fatal("create database directory", zap.Error(err))
	}
	db, err := sql.Open("sqlite3", o.dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_secure_delete=on")
	if err != nil {
		logger.Fatal("open sqlite", zap.Error(err))
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		logger.Fatal("migrate sqlite", zap.Error(err))
	}
	if err := os.Chmod(o.dbPath, 0600); err != nil {
		logger.Fatal("set database permissions", zap.Error(err))
	}

	service, err := control.NewWithLogDir(store.New(db), logger, o.logDir)
	if err != nil {
		logger.Fatal("initialize access log adapter", zap.Error(err))
	}
	service.SetBootstrapInfo(Version, o.mode, o.southboundAddr)
	service.SetEmbeddedNode(o.mode == modeAllInOne)
	service.SetOverlayConfig(overlayCfg)
	service.SetLogIngestLimits(o.logIngestMaxConcurrent, o.logIngestRateBytesPerSecond)
	service.SetTunnelEventStoreCapacity(o.tunnelEventStoreMaxBytes)
	service.SetTunnelEventRetention(o.tunnelEventRetentionDays)
	service.SetClockSkewWarnThreshold(o.clockSkewWarnThresholdMillis)
	service.SetPathActiveProbe(o.pathActiveProbe)

	consoleHostnames, err := parseConsoleHostnames(o.consoleHostnames)
	if err != nil {
		logger.Fatal("parse -console-hostnames", zap.Error(err))
	}
	// The registry, the embedded local node's engine wiring, and the console's and southbound's own
	// registrations all have to be in place before StartAutoSites runs any site: an auto-started site sharing
	// -addr or -southbound-addr admits (or rejects) a hostname-less registration based on whether an owner is
	// already on that address (see internal/sharedport's admit()), and that only holds if the console and
	// southbound got there first.
	registry := sharedport.NewRegistry()
	service.SetSharedPortRegistry(registry)
	if err := registry.PutPlaintextOwner(o.serverAddr, consoleHandler(logger, service, o.webDir), consoleHostnames); err != nil {
		logger.Fatal("register control console", zap.Error(err))
	}
	var southbound *http.Server
	if o.southboundAddr != "" {
		southbound = startSouthbound(ctx, logger, service, registry, o.southboundAddr, o.southboundMaxStreamsPerConn)
	}
	logger.Info("control API listening", zap.String("addr", o.serverAddr), zap.String("mode", o.mode), zap.String("version", Version))
	if err := service.StartAutoSites(context.Background()); err != nil {
		logger.Fatal("load auto-start sites", zap.Error(err))
	}
	<-ctx.Done()
	if southbound != nil {
		// Watch streams never go idle, so graceful shutdown would only wait; nodes reconnect on their own.
		// Server.Close also closes every listener it Serve-d (both the exact-SNI and the default-SNI shared-port
		// registrations startSouthbound made), unregistering southbound from the registry.
		_ = southbound.Close()
	}
	// Unregister the console, then stop every site: internal/sharedport.Registry frees a shared address's real
	// listener synchronously (with a bounded best-effort graceful drain of in-flight requests in the
	// background) once nothing — owner or site — is left registered on it, so there is no separate "close the
	// listener" step to take here.
	registry.RemovePlaintextOwner(o.serverAddr)
	service.StopAll()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 30*time.Second)
	if err := service.Close(drainCtx); err != nil {
		logger.Warn("log store drain or close failed", zap.Error(err))
	}
	cancelDrain()
}

// startSouthbound serves the southbound API through registry instead of its own raw listener, so it can share
// -southbound-addr with the console (-addr) or a site when their normalized addresses coincide (see
// docs/architecture/control-data-plane.md §5). It registers twice at addr, through the same *http.Server and
// the same TLS config: once as the exact-SNI TLS owner for pki.ControllerName (every southbound call a current
// node makes, including its first-ever registration, dials with that SNI pinned — see
// pki.BootstrapClientConfig's and pki.Identity.ControllerClientConfig's doc comments), and once as the address's
// default TLS owner (internal/sharedport.Registry.PutDefaultTLSOwner) — the fallback for a connection that sends
// no SNI at all, or one that does not match pki.ControllerName, which only a node old enough to predate pinning
// the bootstrap SNI still sends (see pki.BootstrapClientConfig's doc comment for exactly what that node sends,
// which is not simply "no SNI for an IP -controller URL": that describes the older client, not the current one).
func startSouthbound(ctx context.Context, logger *zap.Logger, service *control.Control, registry *sharedport.Registry, addr string, maxStreamsPerConn int) *http.Server {
	tlsConfig, err := service.SouthboundTLSConfig(ctx)
	if err != nil {
		logger.Fatal("prepare southbound TLS", zap.Error(err))
	}
	ownerListener, err := registry.PutTLSOwner(addr, pki.ControllerName, tlsConfig)
	if err != nil {
		logger.Fatal("register southbound API", zap.Error(err))
	}
	defaultListener, err := registry.PutDefaultTLSOwner(addr, tlsConfig)
	if err != nil {
		logger.Fatal("register southbound API (default SNI fallback)", zap.Error(err))
	}
	server := &http.Server{
		Handler: service.SouthboundHandler(), ReadHeaderTimeout: control.HeaderTimeout,
		IdleTimeout: 2 * time.Minute, HTTP2: control.SouthboundHTTP2Config(maxStreamsPerConn),
	}
	go func() {
		logger.Info("southbound API listening", zap.String("addr", addr))
		// Serve, not ListenAndServeTLS: ownerListener already hands over completed *tls.Conn values (dispatched
		// by SNI in internal/sharedport), so the handshake must not run a second time; leaving TLSConfig nil
		// keeps Go's automatic HTTP/2 negotiation working for a caller that terminated TLS itself before calling
		// Serve (mirrors internal/overlay's relay port; see relay.go's startRelay).
		if err := server.Serve(ownerListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("serve southbound API", zap.Error(err))
		}
	}()
	go func() {
		if err := server.Serve(defaultListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("serve southbound API (default SNI fallback)", zap.Error(err))
		}
	}()
	return server
}

func consoleHandler(logger *zap.Logger, service *control.Control, webDir string) http.Handler {
	apiHandler := service.Handler()
	frontend := web.Dist()
	if webDir != "" {
		frontend = os.DirFS(webDir)
	}
	if _, err := fs.Stat(frontend, "index.html"); err != nil {
		logger.Warn("frontend is not built; the admin console will be unavailable", zap.String("web_dir", webDir))
	}
	staticHandler := frontendHandler(frontend)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		staticHandler.ServeHTTP(w, r)
	})
}
