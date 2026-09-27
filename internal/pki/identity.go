package pki

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// Identity is a node's certificate, private key, and the CA it trusts. The certificate can be replaced at runtime
// when it is renewed; TLS configs built from the identity pick up the replacement for new handshakes.
type Identity struct {
	NodeID      string
	CAPEM       string
	pool        *x509.CertPool
	certificate atomic.Pointer[tls.Certificate]
}

// LoadIdentity parses a node certificate, its key, and the CA certificate.
func LoadIdentity(certPEM, keyPEM, caPEM string) (*Identity, error) {
	ca, err := ParseCertificate(caPEM)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	identity := &Identity{CAPEM: caPEM, pool: pool}
	if err := identity.SetCertificate(certPEM, keyPEM); err != nil {
		return nil, err
	}
	return identity, nil
}

// SetCertificate replaces the node certificate after checking it chains to the CA and names a node.
func (id *Identity) SetCertificate(certPEM, keyPEM string) error {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return fmt.Errorf("node certificate: %w", err)
	}
	nodeID, ok := NodeIDFromCertificate(pair.Leaf)
	if !ok {
		return errors.New("certificate does not name a node")
	}
	if id.NodeID != "" && id.NodeID != nodeID {
		return fmt.Errorf("certificate names node %q, want %q", nodeID, id.NodeID)
	}
	if _, err := pair.Leaf.Verify(x509.VerifyOptions{Roots: id.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("node certificate is not issued by the CA: %w", err)
	}
	id.NodeID = nodeID
	id.certificate.Store(&pair)
	return nil
}

// Certificate returns the current node certificate.
func (id *Identity) Certificate() *tls.Certificate {
	return id.certificate.Load()
}

// PrivateKey returns the node's ECDSA key, used to request a renewed certificate for the same key.
func (id *Identity) PrivateKey() (*ecdsa.PrivateKey, error) {
	key, ok := id.Certificate().PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("node key is not ECDSA")
	}
	return key, nil
}

// Pool trusts only the internal CA.
func (id *Identity) Pool() *x509.CertPool {
	return id.pool
}

func (id *Identity) clientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return id.certificate.Load(), nil
}

// ControllerClientConfig authenticates the node to the controller and verifies the controller's certificate.
func (id *Identity) ControllerClientConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetClientCertificate: id.clientCertificate, RootCAs: id.pool, ServerName: ControllerName}
}

// PeerClientConfig authenticates the node to the relay port of peer and verifies that it is that node.
func (id *Identity) PeerClientConfig(peer string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetClientCertificate: id.clientCertificate, RootCAs: id.pool, ServerName: NodeName(peer), NextProtos: []string{"h2"}}
}

// RelayServerConfig serves the node's relay port to peers holding certificates from the same CA.
func (id *Identity) RelayServerConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return id.certificate.Load(), nil },
		ClientAuth:     tls.RequireAndVerifyClientCert, ClientCAs: id.pool,
		NextProtos: []string{"h2"},
	}
}

// PeerNodeID identifies the node on the other side of a mutually authenticated connection.
func PeerNodeID(state *tls.ConnectionState) (string, bool) {
	if state == nil || len(state.PeerCertificates) == 0 {
		return "", false
	}
	return NodeIDFromCertificate(state.PeerCertificates[0])
}

// BootstrapClientConfig lets a node that holds only a join token authenticate the controller: the controller must
// present a chain containing the CA with the pinned fingerprint and a certificate for ControllerName from it.
func BootstrapClientConfig(caFingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by the pinned-CA check below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			pool := x509.NewCertPool()
			found := false
			for _, raw := range rawCerts {
				if Fingerprint(raw) == caFingerprint {
					certificate, err := x509.ParseCertificate(raw)
					if err != nil {
						return err
					}
					pool.AddCert(certificate)
					found = true
				}
			}
			if !found {
				return errors.New("controller did not present the CA pinned by the join token")
			}
			_, err := VerifyPeer(rawCerts, pool, ControllerName, x509.ExtKeyUsageServerAuth)
			return err
		},
	}
}

const joinTokenPrefix = "rpop1"

// JoinToken is a single-use credential that lets a node register once.
type JoinToken struct {
	NodeID        string
	Secret        string
	CAFingerprint string
}

func (t JoinToken) String() string {
	return strings.Join([]string{joinTokenPrefix, t.NodeID, t.Secret, t.CAFingerprint}, ".")
}

// ParseJoinToken decodes "rpop1.<node>.<secret>.<ca-fingerprint>".
func ParseJoinToken(value string) (JoinToken, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 4 || parts[0] != joinTokenPrefix || parts[1] == "" || len(parts[2]) < 32 || len(parts[3]) != 64 {
		return JoinToken{}, errors.New("join token must look like rpop1.<node>.<secret>.<ca-fingerprint>")
	}
	return JoinToken{NodeID: parts[1], Secret: parts[2], CAFingerprint: parts[3]}, nil
}
