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

	"github.com/rpop-project/rpop/internal/control"
	"github.com/rpop-project/rpop/internal/store"
	"github.com/rpop-project/rpop/web"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	serverAddr := flag.String("addr", envDefault("RPOP_ADDR", "127.0.0.1:8080"), "control API listen address (env RPOP_ADDR)")
	webDir := flag.String("web-dir", envDefault("RPOP_WEB_DIR", ""), "serve the frontend from this directory instead of the embedded build (env RPOP_WEB_DIR)")
	dbPath := flag.String("db", envDefault("RPOP_DB", "data/rpop.db"), "SQLite database path (env RPOP_DB)")
	logDir := flag.String("log-dir", envDefault("RPOP_LOG_DIR", "logs"), "directory for application logs (env RPOP_LOG_DIR)")
	healthCheck := flag.Bool("health-check", false, "probe the control API at -addr and exit 0 when healthy (for container health checks)")
	flag.Parse()

	if *healthCheck {
		if err := runHealthCheck(*serverAddr); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0700); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*logDir, 0750); err != nil {
		log.Fatal(err)
	}
	loggerConfig := zap.NewProductionConfig()
	// Mirror the diagnostic log to stderr so container runtimes (docker logs) show it.
	loggerConfig.OutputPaths = []string{"stderr", filepath.Join(*logDir, "rpop.log")}
	logger, err := loggerConfig.Build()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(*logDir, "rpop.log"), 0640); err != nil {
		logger.Fatal("set log permissions", zap.Error(err))
	}
	defer logger.Sync()

	db, err := sql.Open("sqlite3", *dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_secure_delete=on")
	if err != nil {
		logger.Fatal("open sqlite", zap.Error(err))
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		logger.Fatal("migrate sqlite", zap.Error(err))
	}
	if err := os.Chmod(*dbPath, 0600); err != nil {
		logger.Fatal("set database permissions", zap.Error(err))
	}

	service, err := control.NewWithLogDir(store.New(db), logger, *logDir)
	if err != nil {
		logger.Fatal("initialize access log adapter", zap.Error(err))
	}
	if err := service.StartAutoSites(context.Background()); err != nil {
		logger.Fatal("load auto-start sites", zap.Error(err))
	}
	apiHandler := service.Handler()
	frontend := web.Dist()
	if *webDir != "" {
		frontend = os.DirFS(*webDir)
	}
	if _, err := fs.Stat(frontend, "index.html"); err != nil {
		logger.Warn("frontend is not built; the admin console will be unavailable", zap.String("web_dir", *webDir))
	}
	staticHandler := frontendHandler(frontend)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		staticHandler.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: *serverAddr, Handler: handler, ReadHeaderTimeout: control.HeaderTimeout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("control API listening", zap.String("addr", *serverAddr), zap.String("version", Version))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("serve control API", zap.Error(err))
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("control API shutdown failed", zap.Error(err))
	}
	cancelShutdown()
	service.StopAll()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 30*time.Second)
	if err := service.CloseAccessLogs(drainCtx); err != nil {
		logger.Warn("access log drain or close failed", zap.Error(err))
	}
	cancelDrain()
}
