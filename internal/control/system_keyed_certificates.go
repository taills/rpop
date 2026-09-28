package control

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rpop-project/rpop/internal/snapshot"
)

const (
	maxSystemKeyedCertificates      = 64
	maxSystemKeyedCertificateBytes  = 64 << 10
	maxSystemKeyedPrivateKeyBytes   = 32 << 10
	maxSystemKeyedCertificateTotals = 1 << 20
)

var stableCertificateIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// systemKeyedCertificate is a certificate chain with its private key. PrivateKeyPEM is write-only and never
// returned by the API.
type systemKeyedCertificate struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	CertificatePEM string `json:"certificatePem"`
	PrivateKeyPEM  string `json:"privateKeyPem,omitempty"`
}

type (
	systemClientCertificate = systemKeyedCertificate
	systemServerCertificate = systemKeyedCertificate
)

// keyedCertificateKind describes how one list of keyed certificates is validated and identified.
type keyedCertificateKind struct {
	field string
	label string
	usage x509.ExtKeyUsage
	// stableIDs keeps a random ID across content replacement so shared certificates can be renewed in place;
	// otherwise the ID is the leaf fingerprint.
	stableIDs bool
	// list returns the settings list that holds certificates of this kind.
	list func(systemSettings) []systemKeyedCertificate
}

var (
	clientCertificateKind = keyedCertificateKind{
		field: "clientCertificates", label: "client", usage: x509.ExtKeyUsageClientAuth,
		list: func(settings systemSettings) []systemKeyedCertificate { return settings.ClientCertificates },
	}
	serverCertificateKind = keyedCertificateKind{
		field: "serverCertificates", label: "server", usage: x509.ExtKeyUsageServerAuth, stableIDs: true,
		list: func(settings systemSettings) []systemKeyedCertificate { return settings.ServerCertificates },
	}
)

type systemKeyedCertificateView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CertificatePEM string   `json:"certificatePem"`
	Subject        string   `json:"subject"`
	Issuer         string   `json:"issuer"`
	DNSNames       []string `json:"dnsNames"`
	Fingerprint    string   `json:"fingerprint"`
	NotBefore      string   `json:"notBefore"`
	NotAfter       string   `json:"notAfter"`
	HasPrivateKey  bool     `json:"hasPrivateKey"`
}

type systemSettingsResponse struct {
	TimeZone           string                       `json:"timeZone"`
	RootCertificates   []systemRootCertificate      `json:"rootCertificates"`
	ClientCertificates []systemKeyedCertificateView `json:"clientCertificates"`
	ServerCertificates []systemKeyedCertificateView `json:"serverCertificates"`
	NodeControllerURL  string                       `json:"nodeControllerUrl"`
	NodeImage          string                       `json:"nodeImage"`
}

func normalizeSystemClientCertificates(certificates []systemKeyedCertificate) ([]systemKeyedCertificate, error) {
	return normalizeKeyedCertificates(clientCertificateKind, certificates)
}

func normalizeSystemServerCertificates(certificates []systemKeyedCertificate) ([]systemKeyedCertificate, error) {
	return normalizeKeyedCertificates(serverCertificateKind, certificates)
}

func normalizeKeyedCertificates(kind keyedCertificateKind, certificates []systemKeyedCertificate) ([]systemKeyedCertificate, error) {
	if len(certificates) > maxSystemKeyedCertificates {
		return nil, fmt.Errorf("%s cannot contain more than %d certificates", kind.field, maxSystemKeyedCertificates)
	}
	normalized := make([]systemKeyedCertificate, 0, len(certificates))
	seenFingerprints := make(map[string]struct{}, len(certificates))
	seenIDs := make(map[string]struct{}, len(certificates))
	totalBytes := 0
	for index, item := range certificates {
		certificatePEM := strings.TrimSpace(item.CertificatePEM)
		keyPEM := strings.TrimSpace(item.PrivateKeyPEM)
		totalBytes += len(certificatePEM) + len(keyPEM)
		if totalBytes > maxSystemKeyedCertificateTotals {
			return nil, fmt.Errorf("%s exceed the %d-byte total limit", kind.field, maxSystemKeyedCertificateTotals)
		}
		chain, err := parseCertificateChain(certificatePEM)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].certificatePem %w", kind.field, index, err)
		}
		leaf := chain[0]
		if leaf.IsCA {
			return nil, fmt.Errorf("%s[%d] is a CA certificate, not a %s certificate", kind.field, index, kind.label)
		}
		if !allowsExtKeyUsage(leaf, kind.usage) {
			return nil, fmt.Errorf("%s[%d] extended key usage does not permit TLS %s authentication", kind.field, index, kind.label)
		}
		if keyPEM == "" {
			return nil, fmt.Errorf("%s[%d].privateKeyPem is required", kind.field, index)
		}
		if len(keyPEM) > maxSystemKeyedPrivateKeyBytes {
			return nil, fmt.Errorf("%s[%d].privateKeyPem must be at most %d bytes", kind.field, index, maxSystemKeyedPrivateKeyBytes)
		}
		if hasEncryptedPEMBlock(keyPEM) {
			return nil, fmt.Errorf("%s[%d].privateKeyPem must be an unencrypted PEM private key", kind.field, index)
		}
		encodedChain := encodeCertificateChain(chain)
		if _, err := tls.X509KeyPair([]byte(encodedChain), []byte(keyPEM)); err != nil {
			return nil, fmt.Errorf("%s[%d] certificate and private key do not form a valid pair: %w", kind.field, index, err)
		}
		fingerprint := certificateFingerprint(leaf)
		if _, duplicate := seenFingerprints[fingerprint]; duplicate {
			return nil, fmt.Errorf("%s contains a duplicate certificate at index %d", kind.field, index)
		}
		seenFingerprints[fingerprint] = struct{}{}
		id, err := keyedCertificateID(kind, item.ID, fingerprint)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].id %w", kind.field, index, err)
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, fmt.Errorf("%s contains a duplicate id at index %d", kind.field, index)
		}
		seenIDs[id] = struct{}{}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = leaf.Subject.CommonName
		}
		if name == "" {
			name = strings.ToUpper(kind.label[:1]) + kind.label[1:] + " " + fingerprint[:12]
		}
		if len(name) > 128 {
			return nil, fmt.Errorf("%s[%d].name must be at most 128 bytes", kind.field, index)
		}
		normalized = append(normalized, systemKeyedCertificate{ID: id, Name: name, CertificatePEM: encodedChain, PrivateKeyPEM: keyPEM + "\n"})
	}
	return normalized, nil
}

func keyedCertificateID(kind keyedCertificateKind, requested, fingerprint string) (string, error) {
	if !kind.stableIDs {
		return fingerprint, nil
	}
	if requested == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("could not be generated: %w", err)
		}
		return hex.EncodeToString(random[:]), nil
	}
	if !stableCertificateIDPattern.MatchString(requested) {
		return "", fmt.Errorf("must be 32 lowercase hexadecimal characters")
	}
	return requested, nil
}

func certificateFingerprint(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:])
}

// parseCertificateChain accepts a leaf certificate optionally followed by intermediates, and nothing else.
func parseCertificateChain(text string) ([]*x509.Certificate, error) {
	if text == "" || len(text) > maxSystemKeyedCertificateBytes {
		return nil, fmt.Errorf("must be non-empty and at most %d bytes", maxSystemKeyedCertificateBytes)
	}
	if !strings.HasPrefix(text, "-----BEGIN CERTIFICATE-----") {
		return nil, fmt.Errorf("must start with a CERTIFICATE PEM block")
	}
	var chain []*x509.Certificate
	rest := []byte(text)
	for len(strings.TrimSpace(string(rest))) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("must contain only CERTIFICATE PEM blocks")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("contains an invalid X.509 certificate: %w", err)
		}
		chain = append(chain, certificate)
	}
	return chain, nil
}

func hasEncryptedPEMBlock(text string) bool {
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if block.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") {
			return true
		}
	}
}

func allowsExtKeyUsage(certificate *x509.Certificate, usage x509.ExtKeyUsage) bool {
	if len(certificate.ExtKeyUsage) == 0 && len(certificate.UnknownExtKeyUsage) == 0 {
		return true
	}
	for _, allowed := range certificate.ExtKeyUsage {
		if allowed == usage || allowed == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func encodeCertificateChain(chain []*x509.Certificate) string {
	var builder strings.Builder
	for _, certificate := range chain {
		builder.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	}
	return builder.String()
}

// mergeKeyedCertificateKeys lets the UI omit write-only private keys: a nil list keeps the current
// certificates, and an entry without a key reuses the stored key of the entry with the same ID.
func mergeKeyedCertificateKeys(requested []systemKeyedCertificate, current []systemKeyedCertificate) []systemKeyedCertificate {
	if requested == nil {
		return append([]systemKeyedCertificate{}, current...)
	}
	keys := make(map[string]string, len(current))
	for _, certificate := range current {
		keys[certificate.ID] = certificate.PrivateKeyPEM
	}
	merged := make([]systemKeyedCertificate, 0, len(requested))
	for _, certificate := range requested {
		if strings.TrimSpace(certificate.PrivateKeyPEM) == "" && certificate.ID != "" {
			certificate.PrivateKeyPEM = keys[certificate.ID]
		}
		merged = append(merged, certificate)
	}
	return merged
}

func keyedCertificateViews(certificates []systemKeyedCertificate) []systemKeyedCertificateView {
	views := make([]systemKeyedCertificateView, 0, len(certificates))
	for _, certificate := range certificates {
		view := systemKeyedCertificateView{
			ID: certificate.ID, Name: certificate.Name, CertificatePEM: certificate.CertificatePEM,
			DNSNames: []string{}, HasPrivateKey: certificate.PrivateKeyPEM != "",
		}
		if chain, err := parseCertificateChain(strings.TrimSpace(certificate.CertificatePEM)); err == nil {
			leaf := chain[0]
			view.Subject = leaf.Subject.String()
			view.Issuer = leaf.Issuer.String()
			view.DNSNames = append(view.DNSNames, leaf.DNSNames...)
			view.Fingerprint = certificateFingerprint(leaf)
			view.NotBefore = leaf.NotBefore.UTC().Format(time.RFC3339)
			view.NotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
		}
		views = append(views, view)
	}
	return views
}

func systemSettingsView(settings systemSettings) systemSettingsResponse {
	return systemSettingsResponse{
		TimeZone:           settings.TimeZone,
		RootCertificates:   append([]systemRootCertificate{}, settings.RootCertificates...),
		ClientCertificates: keyedCertificateViews(settings.ClientCertificates),
		ServerCertificates: keyedCertificateViews(settings.ServerCertificates),
		NodeControllerURL:  settings.NodeControllerURL,
		NodeImage:          settings.NodeImage,
	}
}

func findKeyedCertificate(certificates []systemKeyedCertificate, id string) (systemKeyedCertificate, bool) {
	for _, certificate := range certificates {
		if certificate.ID == id {
			return certificate, true
		}
	}
	return systemKeyedCertificate{}, false
}

func (c *Control) selectedKeyedCertificate(kind keyedCertificateKind, id string) (*tls.Certificate, error) {
	if id == "" {
		return nil, nil
	}
	c.systemSettingsMu.RLock()
	selected, found := findKeyedCertificate(kind.list(c.systemSettings), id)
	c.systemSettingsMu.RUnlock()
	if !found {
		return nil, fmt.Errorf("system %s certificate %q does not exist", kind.label, id)
	}
	pair, err := tls.X509KeyPair([]byte(selected.CertificatePEM), []byte(selected.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("system %s certificate %q is invalid: %w", kind.label, id, err)
	}
	return &pair, nil
}

func (c *Control) selectedClientCertificate(id string) (*tls.Certificate, error) {
	return c.selectedKeyedCertificate(clientCertificateKind, id)
}

func (c *Control) selectedServerCertificate(id string) (*tls.Certificate, error) {
	return c.selectedKeyedCertificate(serverCertificateKind, id)
}

// selectedKeyPair returns the PEM material of a system certificate after checking that it parses.
func (c *Control) selectedKeyPair(kind keyedCertificateKind, id string) (*snapshot.KeyPair, error) {
	if _, err := c.selectedKeyedCertificate(kind, id); err != nil {
		return nil, err
	}
	c.systemSettingsMu.RLock()
	selected, _ := findKeyedCertificate(kind.list(c.systemSettings), id)
	c.systemSettingsMu.RUnlock()
	return &snapshot.KeyPair{CertificatePEM: selected.CertificatePEM, PrivateKeyPEM: selected.PrivateKeyPEM}, nil
}
