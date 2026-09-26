package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/store"
	"go.uber.org/zap"
)

const (
	loggingAdaptersSettingKey = "access_log_adapters"
	legacyLoggingSettingKey   = "access_log_config"
	loggingSiteMigrationKey   = "access_log_site_adapter_migration_v1"
)

type adapterMutation struct {
	Name   string           `json:"name"`
	Config accesslog.Config `json:"config"`
}

type loggingConfigResponse struct {
	Adapters       []loggingAdapterResponse `json:"adapters"`
	SavedAdapterID string                   `json:"savedAdapterId,omitempty"`
}

type loggingAdapterResponse struct {
	ID                    string           `json:"id"`
	Name                  string           `json:"name"`
	Config                accesslog.Config `json:"config"`
	HasClickHousePassword bool             `json:"hasClickHousePassword"`
	HasS3Credentials      bool             `json:"hasS3Credentials"`
}

func NewWithLogDir(s *store.Store, logger *zap.Logger, logDir string) (*Control, error) {
	configs, err := loadLoggingAdapters(context.Background(), s)
	if err != nil {
		return nil, err
	}
	if err := migrateSiteLogAdapterSelection(context.Background(), s, configs); err != nil {
		return nil, err
	}
	registry, err := accesslog.NewRegistry(logDir, configs)
	if err != nil {
		return nil, err
	}
	c := New(s, logger)
	c.accessLogs = registry
	return c, nil
}

func loadLoggingAdapters(ctx context.Context, s *store.Store) ([]accesslog.AdapterConfig, error) {
	raw, err := s.GetSetting(ctx, loggingAdaptersSettingKey)
	if err == nil {
		var configs []accesslog.AdapterConfig
		if err := json.Unmarshal(raw, &configs); err != nil {
			return nil, fmt.Errorf("decode access log adapters: %w", err)
		}
		if configs == nil {
			configs = []accesslog.AdapterConfig{}
		}
		return configs, nil
	}
	if err != store.ErrSettingNotFound {
		return nil, err
	}

	config := accesslog.DefaultConfig()
	legacy, legacyErr := s.GetSetting(ctx, legacyLoggingSettingKey)
	if legacyErr == nil {
		if err := json.Unmarshal(legacy, &config); err != nil {
			return nil, fmt.Errorf("decode legacy access log config: %w", err)
		}
	} else if legacyErr != store.ErrSettingNotFound {
		return nil, legacyErr
	}
	configs := []accesslog.AdapterConfig{{ID: "default", Name: "默认访问日志", Config: config}}
	if err := persistLoggingAdapters(ctx, s, configs); err != nil {
		return nil, err
	}
	return configs, nil
}

func migrateSiteLogAdapterSelection(ctx context.Context, s *store.Store, adapters []accesslog.AdapterConfig) error {
	if _, err := s.GetSetting(ctx, loggingSiteMigrationKey); err == nil {
		return nil
	} else if err != store.ErrSettingNotFound {
		return err
	}
	defaultExists := false
	for _, adapter := range adapters {
		if adapter.ID == "default" {
			defaultExists = true
			break
		}
	}
	if defaultExists {
		sites, err := s.List(ctx)
		if err != nil {
			return err
		}
		updates := make([]store.Site, 0, len(sites))
		for _, site := range sites {
			if site.Config.AccessLog.AdapterID != "" {
				continue
			}
			site.Config.AccessLog.AdapterID = "default"
			updates = append(updates, site)
		}
		if len(updates) > 0 {
			if err := s.SaveMany(ctx, updates); err != nil {
				return fmt.Errorf("migrate legacy site access log adapters: %w", err)
			}
		}
	}
	return s.SetSetting(ctx, loggingSiteMigrationKey, []byte("complete"))
}

func persistLoggingAdapters(ctx context.Context, s *store.Store, configs []accesslog.AdapterConfig) error {
	data, err := json.Marshal(configs)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, loggingAdaptersSettingKey, data)
}

func (c *Control) CloseAccessLogs(ctx context.Context) error {
	if err := c.DrainAccessLogs(ctx); err != nil {
		return err
	}
	if c.accessLogs != nil {
		return c.accessLogs.Close()
	}
	return nil
}

func (c *Control) loggingConfig(w http.ResponseWriter, r *http.Request) {
	if c.accessLogs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"access log adapter registry is unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, c.loggingConfigSnapshot())
	case http.MethodPut:
		// Keep the previous endpoint compatible by updating the adapter named "default".
		var config accesslog.Config
		if !decode(w, r, &config) {
			return
		}
		current, ok := c.findLoggingAdapter("default")
		if !ok {
			writeJSON(w, http.StatusConflict, apiError{"the default adapter does not exist; use the adapters API"})
			return
		}
		c.preserveAdapterSecrets(&config, current.Config)
		if err := c.updateLoggingAdapter(r, current.ID, adapterMutation{Name: current.Name, Config: config}); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c.loggingConfigSnapshot())
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

func (c *Control) loggingAdapters(w http.ResponseWriter, r *http.Request) {
	if c.accessLogs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"access log adapter registry is unavailable"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	var request adapterMutation
	if !decode(w, r, &request) {
		return
	}
	id, err := c.newLoggingAdapterID()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{"could not generate adapter id"})
		return
	}
	adapter := accesslog.AdapterConfig{ID: id, Name: request.Name, Config: request.Config}
	if err := c.accessLogs.Add(adapter, c.persistAdapterList(r)); err != nil {
		if errors.Is(err, accesslog.ErrAdapterExists) {
			writeJSON(w, http.StatusConflict, apiError{err.Error()})
		} else {
			writeError(w, err)
		}
		return
	}
	response := c.loggingConfigSnapshot()
	response.SavedAdapterID = id
	writeJSON(w, http.StatusCreated, response)
}

func (c *Control) loggingAdapter(w http.ResponseWriter, r *http.Request) {
	if c.accessLogs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"access log adapter registry is unavailable"})
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/logging/adapters/"), "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var request adapterMutation
		if !decode(w, r, &request) {
			return
		}
		if err := c.updateLoggingAdapter(r, id, request); err != nil {
			if errors.Is(err, accesslog.ErrAdapterNotFound) {
				writeJSON(w, http.StatusNotFound, apiError{err.Error()})
			} else {
				writeError(w, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, c.loggingConfigSnapshot())
	case http.MethodDelete:
		c.opMu.Lock()
		defer c.opMu.Unlock()
		sites, err := c.store.List(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		for _, site := range sites {
			if site.Config.AccessLog.AdapterID == id {
				writeJSON(w, http.StatusConflict, apiError{fmt.Sprintf("adapter is selected by site %q", site.ID)})
				return
			}
		}
		if err := c.DrainAccessLogs(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		err = c.accessLogs.Remove(id, c.persistAdapterList(r))
		if errors.Is(err, accesslog.ErrAdapterNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{err.Error()})
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c.loggingConfigSnapshot())
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

func (c *Control) updateLoggingAdapter(r *http.Request, id string, request adapterMutation) error {
	current, exists := c.findLoggingAdapter(id)
	if !exists {
		return accesslog.ErrAdapterNotFound
	}
	c.preserveAdapterSecrets(&request.Config, current.Config)
	return c.accessLogs.Update(accesslog.AdapterConfig{ID: id, Name: request.Name, Config: request.Config}, c.persistAdapterList(r))
}

func (c *Control) preserveAdapterSecrets(next *accesslog.Config, current accesslog.Config) {
	if next.ClickHouse.Password == "" {
		next.ClickHouse.Password = current.ClickHouse.Password
	}
	if next.S3.AccessKeyID == "" {
		next.S3.AccessKeyID = current.S3.AccessKeyID
	}
	if next.S3.SecretAccessKey == "" {
		next.S3.SecretAccessKey = current.S3.SecretAccessKey
	}
	if next.S3.SessionToken == "" {
		next.S3.SessionToken = current.S3.SessionToken
	}
}

func (c *Control) findLoggingAdapter(id string) (accesslog.AdapterConfig, bool) {
	for _, adapter := range c.accessLogs.List() {
		if adapter.ID == id {
			return adapter, true
		}
	}
	return accesslog.AdapterConfig{}, false
}

func (c *Control) loggingConfigSnapshot() loggingConfigResponse {
	configs := c.accessLogs.List()
	response := loggingConfigResponse{Adapters: make([]loggingAdapterResponse, 0, len(configs))}
	for _, adapter := range configs {
		config := adapter.Config
		response.Adapters = append(response.Adapters, loggingAdapterResponse{
			ID: adapter.ID, Name: adapter.Name,
			Config:                safeAccessLogConfig(config),
			HasClickHousePassword: config.ClickHouse.Password != "",
			HasS3Credentials:      config.S3.AccessKeyID != "" && config.S3.SecretAccessKey != "",
		})
	}
	return response
}

func safeAccessLogConfig(config accesslog.Config) accesslog.Config {
	config.ClickHouse.Password = ""
	config.S3.AccessKeyID = ""
	config.S3.SecretAccessKey = ""
	config.S3.SessionToken = ""
	return config
}

func (c *Control) persistAdapterList(r *http.Request) func([]accesslog.AdapterConfig) error {
	return func(configs []accesslog.AdapterConfig) error {
		data, err := json.Marshal(configs)
		if err != nil {
			return err
		}
		return c.store.SetSetting(r.Context(), loggingAdaptersSettingKey, data)
	}
}

func (c *Control) newLoggingAdapterID() (string, error) {
	for i := 0; i < 4; i++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		id := "adapter-" + hex.EncodeToString(random[:])
		if !c.accessLogs.Has(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique adapter id")
}

func (c *Control) validateAccessLogAdapter(site store.Site) error {
	id := strings.TrimSpace(site.Config.AccessLog.AdapterID)
	if id == "" {
		return nil
	}
	if c.accessLogs == nil || !c.accessLogs.Has(id) {
		return fmt.Errorf("access log adapter %q is not configured", id)
	}
	return nil
}

func (c *Control) searchLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	if c.accessLogs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"access log adapter registry is unavailable"})
		return
	}
	values := r.URL.Query()
	query := accesslog.Query{Text: values.Get("q"), SiteID: values.Get("siteId"), Page: 1, PageSize: 25}
	adapterID := values.Get("adapterId")
	if len(query.Text) > 512 || len(query.SiteID) > 256 || len(adapterID) > 64 {
		writeJSON(w, http.StatusBadRequest, apiError{"search filter is too long"})
		return
	}
	if query.SiteID != "" {
		site, err := c.store.Get(r.Context(), query.SiteID)
		if err != nil {
			writeError(w, err)
			return
		}
		if site.Config.AccessLog.AdapterID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{"access logging is disabled for this site"})
			return
		}
		if adapterID != "" && adapterID != site.Config.AccessLog.AdapterID {
			writeJSON(w, http.StatusBadRequest, apiError{"adapterId does not match the site's selected adapter"})
			return
		}
		adapterID = site.Config.AccessLog.AdapterID
	}
	if adapterID == "" {
		adapters := c.accessLogs.List()
		switch len(adapters) {
		case 0:
			writeJSON(w, http.StatusBadRequest, apiError{"no access log adapters are configured"})
			return
		case 1:
			adapterID = adapters[0].ID
		default:
			writeJSON(w, http.StatusBadRequest, apiError{"adapterId is required when multiple adapters are configured"})
			return
		}
	}
	if !c.accessLogs.Has(adapterID) {
		writeJSON(w, http.StatusBadRequest, apiError{"unknown access log adapter"})
		return
	}
	var err error
	if raw := values.Get("status"); raw != "" {
		query.Status, err = strconv.Atoi(raw)
		if err != nil || query.Status < 100 || query.Status > 599 {
			writeJSON(w, http.StatusBadRequest, apiError{"status must be between 100 and 599"})
			return
		}
	}
	if raw := values.Get("from"); raw != "" {
		query.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{"from must be RFC3339"})
			return
		}
	}
	if raw := values.Get("to"); raw != "" {
		query.To, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{"to must be RFC3339"})
			return
		}
	}
	if !query.From.IsZero() && !query.To.IsZero() && query.From.After(query.To) {
		writeJSON(w, http.StatusBadRequest, apiError{"from must not be later than to"})
		return
	}
	if raw := values.Get("page"); raw != "" {
		query.Page, err = strconv.Atoi(raw)
		if err != nil || query.Page < 1 || query.Page > 100000 {
			writeJSON(w, http.StatusBadRequest, apiError{"page must be between 1 and 100000"})
			return
		}
	}
	if raw := values.Get("pageSize"); raw != "" {
		query.PageSize, err = strconv.Atoi(raw)
		if err != nil || query.PageSize < 1 || query.PageSize > 100 {
			writeJSON(w, http.StatusBadRequest, apiError{"pageSize must be between 1 and 100"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := c.accessLogs.Search(ctx, adapterID, query)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
