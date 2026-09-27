// Package pki is the internal certificate authority that authenticates the controller and data-plane nodes to
// each other. Nodes keep their private keys: they submit CSRs and receive certificates.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const (
	// ControllerName is the DNS name in the controller's southbound certificate. Nodes verify it against the
	// pinned CA instead of the address they dial, so the controller can be reached under any name.
	ControllerName = "controller.rpop"
	nodeNameSuffix = ".nodes.rpop"

	caValidity         = 20 * 365 * 24 * time.Hour
	NodeValidity       = 365 * 24 * time.Hour
	controllerValidity = 5 * 365 * 24 * time.Hour
	clockSkew          = 5 * time.Minute
)

// NodeName is the DNS name that identifies a node in its certificate.
func NodeName(nodeID string) string {
	return nodeID + nodeNameSuffix
}

// NodeIDFromCertificate extracts the node ID from a node certificate.
func NodeIDFromCertificate(certificate *x509.Certificate) (string, bool) {
	for _, name := range certificate.DNSNames {
		if id, ok := strings.CutSuffix(name, nodeNameSuffix); ok && id != "" && !strings.Contains(id, ".") {
			return id, true
		}
	}
	return "", false
}

// Fingerprint is the SHA-256 of a DER certificate in lowercase hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// CA signs node and controller certificates.
type CA struct {
	Certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	CertPEM     string
	KeyPEM      string
}

// NewCA creates a self-signed ECDSA P-256 certificate authority.
func NewCA(commonName string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: now.Add(-clockSkew), NotAfter: now.Add(caValidity),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return LoadCA(encodePEM("CERTIFICATE", der), encodePEM("EC PRIVATE KEY", keyDER))
}

// LoadCA parses a CA certificate and its EC private key.
func LoadCA(certPEM, keyPEM string) (*CA, error) {
	certificate, err := ParseCertificate(certPEM)
	if err != nil {
		return nil, err
	}
	if !certificate.IsCA {
		return nil, errors.New("certificate is not a CA")
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("CA private key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA private key: %w", err)
	}
	return &CA{Certificate: certificate, key: key, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// Fingerprint identifies the CA; join tokens carry it so a node can authenticate the controller on first contact.
func (ca *CA) Fingerprint() string {
	return Fingerprint(ca.Certificate.Raw)
}

// Pool returns a pool that trusts only this CA.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate)
	return pool
}

// SignNode issues a certificate for a node from its CSR. The certificate serves the node's relay port and
// authenticates it as a client to the controller and to other nodes. generation is the node's registration
// count: registering again bumps it, which revokes every certificate of earlier generations.
func (ca *CA) SignNode(csrPEM, nodeID string, generation int64) (*x509.Certificate, string, error) {
	if generation < 1 {
		return nil, "", errors.New("node certificate generation must be positive")
	}
	csr, err := parseCSR(csrPEM)
	if err != nil {
		return nil, "", err
	}
	subject := pkix.Name{CommonName: nodeID, SerialNumber: strconv.FormatInt(generation, 10)}
	return ca.sign(csr.PublicKey, subject, []string{NodeName(nodeID)}, NodeValidity,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
}

// NodeGeneration reads the registration generation SignNode recorded in a node certificate.
func NodeGeneration(certificate *x509.Certificate) (int64, bool) {
	generation, err := strconv.ParseInt(certificate.Subject.SerialNumber, 10, 64)
	return generation, err == nil && generation > 0
}

// IssueController creates the controller's southbound key pair.
func (ca *CA) IssueController() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificate, _, err := ca.sign(&key.PublicKey, pkix.Name{CommonName: ControllerName}, []string{ControllerName}, controllerValidity,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{certificate.Raw, ca.Certificate.Raw}, PrivateKey: key, Leaf: certificate}, nil
}

func (ca *CA) sign(publicKey any, subject pkix.Name, dnsNames []string, validity time.Duration, usage []x509.ExtKeyUsage) (*x509.Certificate, string, error) {
	serial, err := newSerial()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	notAfter := now.Add(validity)
	if notAfter.After(ca.Certificate.NotAfter) {
		notAfter = ca.Certificate.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: subject, DNSNames: dnsNames,
		NotBefore: now.Add(-clockSkew), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usage, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.Certificate, publicKey, ca.key)
	if err != nil {
		return nil, "", err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", err
	}
	return certificate, encodePEM("CERTIFICATE", der), nil
}

// NewKeyAndCSR generates a node private key and a CSR for it.
func NewKeyAndCSR(nodeID string) (keyPEM, csrPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	csrPEM, err = CSRForKey(key, nodeID)
	if err != nil {
		return "", "", err
	}
	return encodePEM("EC PRIVATE KEY", keyDER), csrPEM, nil
}

// CSRForKey creates a CSR for an existing key, used when a node renews its certificate.
func CSRForKey(key *ecdsa.PrivateKey, nodeID string) (string, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID}}, key)
	if err != nil {
		return "", err
	}
	return encodePEM("CERTIFICATE REQUEST", der), nil
}

func parseCSR(csrPEM string) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("csr must be a PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csr signature: %w", err)
	}
	if _, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok {
		return nil, errors.New("csr must use an ECDSA key")
	}
	return csr, nil
}

// ParseCertificate decodes the first certificate of a PEM bundle.
func ParseCertificate(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate must be PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// VerifyPeer checks that a presented chain is issued by pool and names dnsName.
func VerifyPeer(rawCerts [][]byte, pool *x509.CertPool, dnsName string, usage x509.ExtKeyUsage) (*x509.Certificate, error) {
	if len(rawCerts) == 0 {
		return nil, errors.New("peer presented no certificate")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, err
	}
	intermediates := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		if certificate, err := x509.ParseCertificate(raw); err == nil {
			intermediates.AddCert(certificate)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, DNSName: dnsName, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return nil, err
	}
	return leaf, nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func encodePEM(blockType string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}
