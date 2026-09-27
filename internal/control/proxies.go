package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

const (
	proxiesSettingKey    = "proxies"
	maxProxies           = 256
	maxProxyNameBytes    = 128
	maxProxyCredentials  = 255 // SOCKS5 username/password authentication allows at most 255 bytes each
	maxProxyAddressBytes = 255
)

// proxyIDPattern keeps proxy IDs readable in path notation such as "socks5-A > node2".
var proxyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

var proxyTypes = []string{"socks5", "socks5h", "http", "https"}

// namedProxy is an external SOCKS5 or HTTP CONNECT proxy that upstream paths pass through by ID. Password is
// write-only: the API never returns it.
type namedProxy struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Address  string `json:"address"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type proxyView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Address     string   `json:"address"`
	Username    string   `json:"username,omitempty"`
	HasPassword bool     `json:"hasPassword"`
	UsedBy      []string `json:"usedBy"`
}

// proxyMutation is a create or update request. A nil Password keeps the stored one; an empty one clears it.
type proxyMutation struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Address  string  `json:"address"`
	Username string  `json:"username"`
	Password *string `json:"password"`
}

// proxyRegistry caches the stored proxies; writes happen under Control.opMu and replace the cache.
type proxyRegistry struct {
	mu      sync.RWMutex
	loaded  bool
	proxies []namedProxy
}

func (p namedProxy) validate() error {
	if !proxyIDPattern.MatchString(p.ID) {
		return fmt.Errorf("proxy id must be 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	if len(p.Name) > maxProxyNameBytes {
		return fmt.Errorf("proxy name must be at most %d bytes", maxProxyNameBytes)
	}
	if !slices.Contains(proxyTypes, p.Type) {
		return fmt.Errorf("proxy type must be one of %s", strings.Join(proxyTypes, ", "))
	}
	host, port, err := net.SplitHostPort(p.Address)
	if err != nil || host == "" || len(p.Address) > maxProxyAddressBytes {
		return fmt.Errorf("proxy address must be host:port")
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("proxy address port must be 1-65535")
	}
	if len(p.Username) > maxProxyCredentials || len(p.Password) > maxProxyCredentials {
		return fmt.Errorf("proxy username and password must be at most %d bytes each", maxProxyCredentials)
	}
	return nil
}

func (p namedProxy) snapshot() snapshot.Proxy {
	return snapshot.Proxy{Type: p.Type, Address: p.Address, Username: p.Username, Password: p.Password}
}

// loadedProxies returns the stored proxies, reading them on first use.
func (c *Control) loadedProxies(ctx context.Context) ([]namedProxy, error) {
	c.proxies.mu.RLock()
	if c.proxies.loaded {
		defer c.proxies.mu.RUnlock()
		return c.proxies.proxies, nil
	}
	c.proxies.mu.RUnlock()
	c.proxies.mu.Lock()
	defer c.proxies.mu.Unlock()
	if c.proxies.loaded {
		return c.proxies.proxies, nil
	}
	proxies := []namedProxy{}
	raw, err := c.store.GetSetting(ctx, proxiesSettingKey)
	switch {
	case errors.Is(err, store.ErrSettingNotFound):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &proxies); err != nil {
			return nil, fmt.Errorf("decode proxies: %w", err)
		}
	}
	c.proxies.proxies, c.proxies.loaded = proxies, true
	return proxies, nil
}

func (c *Control) saveProxies(ctx context.Context, proxies []namedProxy) error {
	data, err := json.Marshal(proxies)
	if err != nil {
		return err
	}
	c.proxies.mu.Lock()
	defer c.proxies.mu.Unlock()
	if err := c.store.SetSetting(ctx, proxiesSettingKey, data); err != nil {
		return err
	}
	c.proxies.proxies, c.proxies.loaded = proxies, true
	return nil
}

// proxyByID resolves a proxy a path references.
func (c *Control) proxyByID(ctx context.Context, id string) (namedProxy, error) {
	proxies, err := c.loadedProxies(ctx)
	if err != nil {
		return namedProxy{}, err
	}
	for _, p := range proxies {
		if p.ID == id {
			return p, nil
		}
	}
	return namedProxy{}, fmt.Errorf("proxy %q does not exist", id)
}

// sitesUsingProxy lists the sites whose paths pass through a proxy.
func sitesUsingProxy(sites []store.Site, id string) []string {
	users := []string{}
	for _, site := range sites {
		if slices.ContainsFunc(site.Config.Upstreams, func(u store.Upstream) bool {
			return slices.ContainsFunc(upstreamPaths(u), func(p store.UpstreamPath) bool {
				return slices.ContainsFunc(p.Via, func(h store.Hop) bool { return h.Proxy == id })
			})
		}) {
			users = append(users, site.ID)
		}
	}
	return users
}

func proxyViews(proxies []namedProxy, sites []store.Site) []proxyView {
	views := make([]proxyView, 0, len(proxies))
	for _, p := range proxies {
		views = append(views, proxyView{ID: p.ID, Name: p.Name, Type: p.Type, Address: p.Address, Username: p.Username,
			HasPassword: p.Password != "", UsedBy: sitesUsingProxy(sites, p.ID)})
	}
	return views
}

func (c *Control) proxiesAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		proxies, err := c.loadedProxies(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		sites, err := c.store.List(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, proxyViews(proxies, sites))
	case http.MethodPost:
		var input proxyMutation
		if !decode(w, r, &input) {
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		proxies, err := c.loadedProxies(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if slices.ContainsFunc(proxies, func(p namedProxy) bool { return p.ID == input.ID }) {
			writeJSON(w, http.StatusConflict, apiError{fmt.Sprintf("proxy %q already exists", input.ID)})
			return
		}
		if len(proxies) >= maxProxies {
			writeJSON(w, http.StatusBadRequest, apiError{fmt.Sprintf("at most %d proxies can be registered", maxProxies)})
			return
		}
		created := input.apply(namedProxy{ID: input.ID})
		if err := created.validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		if err := c.saveProxies(r.Context(), append(slices.Clone(proxies), created)); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, proxyViews([]namedProxy{created}, nil)[0])
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

func (c *Control) proxyAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/proxies/"), "/")
	c.opMu.Lock()
	defer c.opMu.Unlock()
	proxies, err := c.loadedProxies(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	index := slices.IndexFunc(proxies, func(p namedProxy) bool { return p.ID == id })
	if index < 0 {
		writeJSON(w, http.StatusNotFound, apiError{fmt.Sprintf("proxy %q does not exist", id)})
		return
	}
	sites, err := c.store.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input proxyMutation
		if !decode(w, r, &input) {
			return
		}
		if input.ID != "" && input.ID != id {
			writeJSON(w, http.StatusBadRequest, apiError{"a proxy id cannot be changed"})
			return
		}
		updated := input.apply(proxies[index])
		if err := updated.validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		next := slices.Clone(proxies)
		next[index] = updated
		if err := c.saveProxies(r.Context(), next); err != nil {
			writeError(w, err)
			return
		}
		// Nodes receive proxy definitions inline, so every site that passes through the proxy is rendered again.
		c.reapplyLocked(r.Context())
		writeJSON(w, http.StatusOK, proxyViews([]namedProxy{updated}, sites)[0])
	case http.MethodDelete:
		if users := sitesUsingProxy(sites, id); len(users) > 0 {
			writeJSON(w, http.StatusConflict, apiError{fmt.Sprintf("proxy %q is used by sites %s", id, strings.Join(users, ", "))})
			return
		}
		if err := c.saveProxies(r.Context(), slices.Delete(slices.Clone(proxies), index, index+1)); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}

// apply returns base with the mutation's fields; the password changes only when the request carries one.
func (m proxyMutation) apply(base namedProxy) namedProxy {
	base.Name, base.Type = strings.TrimSpace(m.Name), strings.TrimSpace(m.Type)
	base.Address, base.Username = strings.TrimSpace(m.Address), m.Username
	if m.Password != nil {
		base.Password = *m.Password
	}
	if base.Username == "" {
		base.Password = ""
	}
	return base
}
