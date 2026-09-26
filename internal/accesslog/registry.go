package accesslog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrAdapterNotFound = errors.New("access log adapter not found")
	ErrAdapterExists   = errors.New("access log adapter already exists")
)

type AdapterConfig struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Config Config `json:"config"`
}

type adapterEntry struct {
	config  AdapterConfig
	manager *Manager
}

type Registry struct {
	mu       sync.RWMutex
	dir      string
	adapters map[string]adapterEntry
	location *time.Location
}

var adapterIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func NewRegistry(dir string, configs []AdapterConfig) (*Registry, error) {
	registry := &Registry{dir: dir, adapters: make(map[string]adapterEntry, len(configs)), location: time.UTC}
	for _, config := range configs {
		normalized, err := normalizeAdapterConfig(config)
		if err != nil {
			_ = registry.Close()
			return nil, err
		}
		if _, exists := registry.adapters[normalized.ID]; exists {
			_ = registry.Close()
			return nil, fmt.Errorf("duplicate access log adapter id %q", normalized.ID)
		}
		manager, err := NewManager(registry.adapterDir(normalized.ID), normalized.Config)
		if err != nil {
			_ = registry.Close()
			return nil, fmt.Errorf("initialize access log adapter %q: %w", normalized.ID, err)
		}
		registry.adapters[normalized.ID] = adapterEntry{config: normalized, manager: manager}
	}
	return registry, nil
}

func normalizeAdapterConfig(config AdapterConfig) (AdapterConfig, error) {
	config.ID = strings.TrimSpace(config.ID)
	config.Name = strings.TrimSpace(config.Name)
	if !adapterIDPattern.MatchString(config.ID) {
		return AdapterConfig{}, fmt.Errorf("adapter id must contain 1-64 letters, numbers, underscores, or hyphens")
	}
	if config.Name == "" || len(config.Name) > 128 {
		return AdapterConfig{}, fmt.Errorf("adapter name must contain 1-128 characters")
	}
	config.Config = normalizeConfig(config.Config)
	if err := config.Config.Validate(); err != nil {
		return AdapterConfig{}, err
	}
	return config, nil
}

func (r *Registry) adapterDir(id string) string {
	if id == "default" {
		return r.dir
	}
	return filepath.Join(r.dir, "access-adapters", id)
}

func (r *Registry) List() []AdapterConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listLocked()
}

func (r *Registry) listLocked() []AdapterConfig {
	configs := make([]AdapterConfig, 0, len(r.adapters))
	for _, entry := range r.adapters {
		configs = append(configs, entry.config)
	}
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].Name == configs[j].Name {
			return configs[i].ID < configs[j].ID
		}
		return strings.ToLower(configs[i].Name) < strings.ToLower(configs[j].Name)
	})
	return configs
}

func (r *Registry) SetTimeZone(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.location = location
	for _, entry := range r.adapters {
		entry.manager.SetTimeZone(location)
	}
}

func (r *Registry) Has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.adapters[id]
	return exists
}

func (r *Registry) Add(config AdapterConfig, persist func([]AdapterConfig) error) error {
	return r.put(config, false, persist)
}

func (r *Registry) Update(config AdapterConfig, persist func([]AdapterConfig) error) error {
	return r.put(config, true, persist)
}

func (r *Registry) put(config AdapterConfig, requireExisting bool, persist func([]AdapterConfig) error) error {
	normalized, err := normalizeAdapterConfig(config)
	if err != nil {
		return err
	}
	next, err := NewManager(r.adapterDir(normalized.ID), normalized.Config)
	if err != nil {
		return err
	}
	r.mu.Lock()
	previous, exists := r.adapters[normalized.ID]
	if requireExisting && !exists {
		r.mu.Unlock()
		_ = next.Close()
		return ErrAdapterNotFound
	}
	if !requireExisting && exists {
		r.mu.Unlock()
		_ = next.Close()
		return ErrAdapterExists
	}
	next.SetTimeZone(r.location)
	configs := r.listLocked()
	if exists {
		for i := range configs {
			if configs[i].ID == normalized.ID {
				configs[i] = normalized
				break
			}
		}
	} else {
		configs = append(configs, normalized)
	}
	if persist != nil {
		if err := persist(configs); err != nil {
			r.mu.Unlock()
			_ = next.Close()
			return err
		}
	}
	r.adapters[normalized.ID] = adapterEntry{config: normalized, manager: next}
	r.mu.Unlock()
	if exists {
		return previous.manager.Close()
	}
	return nil
}

func (r *Registry) Remove(id string, persist func([]AdapterConfig) error) error {
	r.mu.Lock()
	previous, exists := r.adapters[id]
	if !exists {
		r.mu.Unlock()
		return ErrAdapterNotFound
	}
	configs := r.listLocked()
	for i := range configs {
		if configs[i].ID == id {
			configs = append(configs[:i], configs[i+1:]...)
			break
		}
	}
	if persist != nil {
		if err := persist(configs); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	delete(r.adapters, id)
	r.mu.Unlock()
	return previous.manager.Close()
}

func (r *Registry) Write(ctx context.Context, id string, record Record) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.adapters[id]
	if !exists {
		return fmt.Errorf("%w: %s", ErrAdapterNotFound, id)
	}
	return entry.manager.Write(ctx, record)
}

func (r *Registry) Search(ctx context.Context, id string, query Query) (SearchResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.adapters[id]
	if !exists {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrAdapterNotFound, id)
	}
	return entry.manager.Search(ctx, query)
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for id, entry := range r.adapters {
		if err := entry.manager.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close access log adapter %q: %w", id, err))
		}
		delete(r.adapters, id)
	}
	return errors.Join(errs...)
}
