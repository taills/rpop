package control

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/rpop-project/rpop/internal/store"
)

const HeaderTimeout = 8 * time.Second

type Control struct {
	store          *store.Store
	log            *zap.Logger
	mu             sync.Mutex
	opMu           sync.Mutex
	runs           map[string]*running
	listeners      map[string]*listenerGroup
	metrics        sync.Map
	accessLogQueue chan accessLogEvent
	queuedLogBytes atomic.Int64
}
type running struct {
	groupKey string
	route    *siteRuntime
}
type siteRuntime struct {
	id          string
	handler     http.Handler
	hostnames   []string
	certificate *tls.Certificate
}
type listenerGroup struct {
	mu           sync.RWMutex
	key, address string
	tlsEnabled   bool
	server       *http.Server
	listener     net.Listener
	routes       map[string]*siteRuntime
	byHost       map[string]*siteRuntime
}
type apiError struct {
	Error string `json:"error"`
}

func New(s *store.Store, l *zap.Logger) *Control {
	c := &Control{store: s, log: l, runs: map[string]*running{}, listeners: map[string]*listenerGroup{}, accessLogQueue: make(chan accessLogEvent, 256)}
	go c.accessLogLoop()
	return c
}
func (c *Control) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/health", c.health)
	m.HandleFunc("/api/config.yaml", c.yamlConfig)
	m.HandleFunc("/api/sites", c.sites)
	m.HandleFunc("/api/sites/", c.site)
	return m
}

type yamlConfig struct {
	Sites []store.Site `yaml:"sites"`
}

func (c *Control) yamlConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sites, err := c.store.List(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		for i := range sites {
			c.mu.Lock()
			_, sites[i].Running = c.runs[sites[i].ID]
			c.mu.Unlock()
		}
		data, err := yaml.Marshal(yamlConfig{Sites: sites})
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=sites.yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodPut:
		defer r.Body.Close()
		decoder := yaml.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.KnownFields(true)
		var cfg yamlConfig
		if err := decoder.Decode(&cfg); err != nil {
			writeJSON(w, 400, apiError{err.Error()})
			return
		}
		for _, site := range cfg.Sites {
			if err := validate(site); err != nil {
				writeError(w, fmt.Errorf("site %q: %w", site.ID, err))
				return
			}
		}
		if err := c.store.SaveMany(r.Context(), cfg.Sites); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"imported": len(cfg.Sites)})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, 405, apiError{"method not allowed"})
	}
}
func (c *Control) health(w http.ResponseWriter, r *http.Request) {
	if err := c.store.Check(r.Context()); err != nil {
		writeJSON(w, 503, apiError{err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (c *Control) sites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		xs, e := c.store.List(r.Context())
		if e != nil {
			writeError(w, e)
			return
		}
		for i := range xs {
			c.mu.Lock()
			_, xs[i].Running = c.runs[xs[i].ID]
			c.mu.Unlock()
		}
		writeJSON(w, 200, xs)
	case http.MethodPost:
		var x store.Site
		if !decode(w, r, &x) {
			return
		}
		if err := validate(x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.store.Save(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, x)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, apiError{"method not allowed"})
	}
}
func (c *Control) site(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	id := parts[2]
	if len(parts) == 4 && parts[3] == "metrics" && r.Method == http.MethodGet {
		if _, err := c.store.Get(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c.metricsForSite(id).snapshot())
		return
	}
	if len(parts) == 5 && parts[3] == "secrets" {
		c.secret(w, r, id, parts[4])
		return
	}
	if len(parts) == 3 && r.Method == http.MethodPut {
		var x store.Site
		if !decode(w, r, &x) {
			return
		}
		x.ID = id
		if err := validate(x); err != nil {
			writeError(w, err)
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		c.mu.Lock()
		active := c.runs[id] != nil
		c.mu.Unlock()
		var previous store.Site
		if active {
			previousSite, getErr := c.store.Get(r.Context(), id)
			if getErr != nil {
				writeError(w, getErr)
				return
			}
			previous = previousSite
		}
		if err := c.store.Save(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		if active {
			if err := c.startLocked(r.Context(), id); err != nil {
				if rollbackErr := c.store.Save(r.Context(), previous); rollbackErr != nil {
					writeError(w, fmt.Errorf("site update failed: %v; restoring previous config failed: %w", err, rollbackErr))
					return
				}
				if rollbackErr := c.startLocked(r.Context(), id); rollbackErr != nil {
					writeError(w, fmt.Errorf("site update failed: %v; restoring previous runtime failed: %w", err, rollbackErr))
					return
				}
				writeError(w, err)
				return
			}
		}
		writeJSON(w, 200, x)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		_ = c.stop(id)
		if err := c.store.Delete(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		c.metrics.Delete(id)
		w.WriteHeader(204)
		return
	}
	if len(parts) == 4 && r.Method == http.MethodPost {
		var err error
		switch parts[3] {
		case "start", "reload", "restart":
			err = c.start(r.Context(), id)
		case "stop":
			err = c.stop(id)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "status": map[string]string{"start": "running", "reload": "reloaded", "restart": "restarted", "stop": "stopped"}[parts[3]]})
		return
	}
	writeJSON(w, 405, apiError{"method not allowed"})
}
func (c *Control) start(ctx context.Context, id string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.startLocked(ctx, id)
}

func (c *Control) StartAutoSites(ctx context.Context) error {
	sites, err := c.store.AutoStartSites(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		if err := c.start(ctx, site.ID); err != nil {
			c.log.Error("auto-start site failed", zap.String("site", site.ID), zap.Error(err))
		}
	}
	return nil
}

func (c *Control) StopAll() {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	ids := make([]string, 0, len(c.runs))
	for id := range c.runs {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		_ = c.stopLocked(id)
	}
}

func (c *Control) stop(id string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.stopLocked(id)
}

func (c *Control) proxyHandler(ctx context.Context, id string, cfg store.Config) (http.Handler, error) {
	if len(cfg.Upstreams) == 0 {
		return nil, fmt.Errorf("at least one upstream is required")
	}
	u, e := url.Parse(cfg.Upstreams[0].URL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid upstream URL")
	}
	transport, e := store.BuildTransport(ctx, c.store, id, cfg.Upstreams[0])
	if e != nil {
		return nil, e
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = u.Host
		},
	}
	return c.observeSite(id, cfg.AccessLog, proxy), nil
}
func validate(x store.Site) error {
	if strings.TrimSpace(x.ID) == "" || strings.TrimSpace(x.Name) == "" {
		return fmt.Errorf("site id and name are required")
	}
	if len(x.Config.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream required")
	}
	if x.Config.ListenPort < 1 || x.Config.ListenPort > 65535 {
		return fmt.Errorf("listenPort must be 1-65535")
	}
	if _, err := normalizedHostnames(x.Config.Hostnames); err != nil {
		return err
	}
	for _, u := range x.Config.Upstreams {
		if _, e := url.ParseRequestURI(u.URL); e != nil {
			return fmt.Errorf("invalid upstream URL")
		}
	}
	return nil
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		writeJSON(w, 400, apiError{e.Error()})
		return false
	}
	return true
}

func (c *Control) secret(w http.ResponseWriter, r *http.Request, siteID, name string) {
	if name == "" || len(name) > 128 {
		writeJSON(w, 400, apiError{"invalid secret name"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		defer r.Body.Close()
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
		if err != nil {
			writeJSON(w, 413, apiError{"secret exceeds 5 MiB"})
			return
		}
		if len(data) == 0 {
			writeJSON(w, 400, apiError{"secret cannot be empty"})
			return
		}
		if _, err = c.store.Get(r.Context(), siteID); err != nil {
			writeError(w, err)
			return
		}
		if err = c.store.SaveSecret(r.Context(), siteID, name, data); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{"name": name, "status": "stored"})
	default:
		w.Header().Set("Allow", "PUT")
		writeJSON(w, 405, apiError{"method not allowed"})
	}
}
func writeError(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, store.ErrNotFound) {
		status = 404
	}
	writeJSON(w, status, apiError{e.Error()})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
