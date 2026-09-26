package control

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/rpop-project/rpop/internal/store"
)

const (
	systemSettingsSettingKey  = "system_settings"
	maxSystemRootCertificates = 64
	maxSystemRootPEMBytes     = 256 << 10
	maxSystemRootBytes        = 1 << 20
)

type systemRootCertificate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	PEM  string `json:"pem"`
}

type systemSettings struct {
	TimeZone           string                    `json:"timeZone"`
	RootCertificates   []systemRootCertificate   `json:"rootCertificates"`
	ClientCertificates []systemClientCertificate `json:"clientCertificates"`
	ServerCertificates []systemServerCertificate `json:"serverCertificates"`
}

func defaultSystemSettings() systemSettings {
	return systemSettings{TimeZone: "UTC", RootCertificates: []systemRootCertificate{}, ClientCertificates: []systemClientCertificate{}, ServerCertificates: []systemServerCertificate{}}
}

func normalizeSystemSettings(settings systemSettings) (systemSettings, *time.Location, error) {
	settings.TimeZone = strings.TrimSpace(settings.TimeZone)
	if settings.TimeZone == "" {
		settings.TimeZone = "UTC"
	}
	if strings.EqualFold(settings.TimeZone, "Local") {
		return systemSettings{}, nil, fmt.Errorf("timeZone must be UTC or a valid IANA timezone, not Local")
	}
	location, err := time.LoadLocation(settings.TimeZone)
	if err != nil {
		return systemSettings{}, nil, fmt.Errorf("timeZone must be UTC or a valid IANA timezone: %w", err)
	}
	settings.RootCertificates, err = normalizeSystemRootCertificates(settings.RootCertificates)
	if err != nil {
		return systemSettings{}, nil, err
	}
	settings.ClientCertificates, err = normalizeSystemClientCertificates(settings.ClientCertificates)
	if err != nil {
		return systemSettings{}, nil, err
	}
	settings.ServerCertificates, err = normalizeSystemServerCertificates(settings.ServerCertificates)
	if err != nil {
		return systemSettings{}, nil, err
	}
	return settings, location, nil
}

func normalizeSystemRootCertificates(certificates []systemRootCertificate) ([]systemRootCertificate, error) {
	if len(certificates) > maxSystemRootCertificates {
		return nil, fmt.Errorf("rootCertificates cannot contain more than %d certificates", maxSystemRootCertificates)
	}
	normalized := make([]systemRootCertificate, 0, len(certificates))
	seen := make(map[string]struct{}, len(certificates))
	totalBytes := 0
	for index, item := range certificates {
		pemText := strings.TrimSpace(item.PEM)
		if len(pemText) == 0 || len(pemText) > maxSystemRootPEMBytes {
			return nil, fmt.Errorf("rootCertificates[%d].pem must be non-empty and at most %d bytes", index, maxSystemRootPEMBytes)
		}
		totalBytes += len(pemText)
		if totalBytes > maxSystemRootBytes {
			return nil, fmt.Errorf("rootCertificates exceed the %d-byte total limit", maxSystemRootBytes)
		}
		block, rest := pem.Decode([]byte(pemText))
		if !strings.HasPrefix(pemText, "-----BEGIN CERTIFICATE-----") || block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
			return nil, fmt.Errorf("rootCertificates[%d].pem must contain exactly one CERTIFICATE PEM block", index)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("rootCertificates[%d] is not a valid X.509 certificate: %w", index, err)
		}
		if !certificate.IsCA {
			return nil, fmt.Errorf("rootCertificates[%d] is not a CA certificate", index)
		}
		fingerprint := sha256.Sum256(certificate.Raw)
		id := hex.EncodeToString(fingerprint[:])
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("rootCertificates contains a duplicate certificate at index %d", index)
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = certificate.Subject.CommonName
		}
		if name == "" {
			name = "CA " + id[:12]
		}
		if len(name) > 128 {
			return nil, fmt.Errorf("rootCertificates[%d].name must be at most 128 bytes", index)
		}
		normalized = append(normalized, systemRootCertificate{
			ID:   id,
			Name: name,
			PEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})),
		})
	}
	return normalized, nil
}

func loadSystemSettings(ctx context.Context, s *store.Store) (systemSettings, *time.Location, error) {
	raw, err := s.GetSetting(ctx, systemSettingsSettingKey)
	if errors.Is(err, store.ErrSettingNotFound) {
		return normalizeSystemSettings(defaultSystemSettings())
	}
	if err != nil {
		return systemSettings{}, nil, err
	}
	settings := defaultSystemSettings()
	if err := json.Unmarshal(raw, &settings); err != nil {
		return systemSettings{}, nil, fmt.Errorf("decode system settings: %w", err)
	}
	return normalizeSystemSettings(settings)
}

func (c *Control) selectedRootCertificates(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	c.systemSettingsMu.RLock()
	certificates := append([]systemRootCertificate(nil), c.systemSettings.RootCertificates...)
	c.systemSettingsMu.RUnlock()
	byID := make(map[string]string, len(certificates))
	for _, certificate := range certificates {
		byID[certificate.ID] = certificate.PEM
	}
	selected := make([]string, 0, len(ids))
	for _, id := range ids {
		pemText, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("system root certificate %q does not exist", id)
		}
		selected = append(selected, pemText)
	}
	return selected, nil
}

func (c *Control) validateSystemCertificateReferences(site store.Site) error {
	if _, err := c.selectedServerCertificate(site.Config.CertificateID); err != nil {
		return err
	}
	for index, upstream := range site.Config.Upstreams {
		if _, err := c.selectedRootCertificates(upstream.RootCertificateIDs); err != nil {
			return fmt.Errorf("upstreams[%d]: %w", index, err)
		}
		if _, err := c.selectedClientCertificate(upstream.ClientCertificateID); err != nil {
			return fmt.Errorf("upstreams[%d]: %w", index, err)
		}
	}
	return nil
}

func validateCertificateReferencesAgainstSettings(ctx context.Context, s *store.Store, settings systemSettings) error {
	sites, err := s.List(ctx)
	if err != nil {
		return err
	}
	availableRoots := make(map[string]struct{}, len(settings.RootCertificates))
	for _, certificate := range settings.RootCertificates {
		availableRoots[certificate.ID] = struct{}{}
	}
	availableClients := make(map[string]struct{}, len(settings.ClientCertificates))
	for _, certificate := range settings.ClientCertificates {
		availableClients[certificate.ID] = struct{}{}
	}
	availableServers := make(map[string]struct{}, len(settings.ServerCertificates))
	for _, certificate := range settings.ServerCertificates {
		availableServers[certificate.ID] = struct{}{}
	}
	for _, site := range sites {
		if id := site.Config.CertificateID; id != "" {
			if _, ok := availableServers[id]; !ok {
				return fmt.Errorf("cannot remove system server certificate %q; it is selected by site %q", id, site.ID)
			}
		}
		for upstreamIndex, upstream := range site.Config.Upstreams {
			for _, id := range upstream.RootCertificateIDs {
				if _, ok := availableRoots[id]; !ok {
					return fmt.Errorf("cannot remove system root certificate %q; it is selected by site %q upstream %d", id, site.ID, upstreamIndex+1)
				}
			}
			if id := upstream.ClientCertificateID; id != "" {
				if _, ok := availableClients[id]; !ok {
					return fmt.Errorf("cannot remove system client certificate %q; it is selected by site %q upstream %d", id, site.ID, upstreamIndex+1)
				}
			}
		}
	}
	return nil
}

func (c *Control) systemSettingsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c.systemSettingsMu.RLock()
		view := systemSettingsView(c.systemSettings)
		c.systemSettingsMu.RUnlock()
		writeJSON(w, http.StatusOK, view)
	case http.MethodPut:
		var requested systemSettings
		if !decode(w, r, &requested) {
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		c.systemSettingsMu.RLock()
		requested.ClientCertificates = mergeKeyedCertificateKeys(requested.ClientCertificates, c.systemSettings.ClientCertificates)
		requested.ServerCertificates = mergeKeyedCertificateKeys(requested.ServerCertificates, c.systemSettings.ServerCertificates)
		c.systemSettingsMu.RUnlock()
		settings, location, err := normalizeSystemSettings(requested)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		data, err := json.Marshal(settings)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := validateCertificateReferencesAgainstSettings(r.Context(), c.store, settings); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		c.systemSettingsMu.Lock()
		if err := c.store.SetSetting(r.Context(), systemSettingsSettingKey, data); err != nil {
			c.systemSettingsMu.Unlock()
			writeError(w, err)
			return
		}
		if c.accessLogs != nil {
			c.accessLogs.SetTimeZone(location)
		}
		serverCertificatesChanged := !slices.Equal(c.systemSettings.ServerCertificates, settings.ServerCertificates)
		c.systemSettings = settings
		c.systemSettingsMu.Unlock()
		if serverCertificatesChanged {
			c.refreshSharedServerCertificates()
		}
		writeJSON(w, http.StatusOK, systemSettingsView(settings))
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
	}
}
