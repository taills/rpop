// Package snapshot defines the desired state the controller hands to a data-plane node. Every reference is
// resolved: certificates, keys, and CA roots are inlined, so a node needs nothing but the snapshot to serve.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/rpop-project/rpop/internal/routing"
)

// Snapshot is the complete desired state of one node at a controller revision.
type Snapshot struct {
	Revision int64  `json:"revision"`
	NodeID   string `json:"nodeId"`
	Sites    []Site `json:"sites"`
	// Peers are the nodes this node dials or accepts tunnels from.
	Peers []Peer `json:"peers,omitempty"`
	// RelayListen is the address the node's relay port binds; it is set when Relay has routes.
	RelayListen string `json:"relayListen,omitempty"`
	// Relay is the route table of the relay port: the only tunnels it forwards.
	Relay []RelayRoute `json:"relay,omitempty"`
}

// Peer is another node. Address is where its relay port is reached; Generation is the registration generation
// its certificate must carry, which revokes certificates of earlier registrations.
type Peer struct {
	ID         string `json:"id"`
	Address    string `json:"address,omitempty"`
	Generation int64  `json:"generation"`
}

// Proxy is an external SOCKS5 or HTTP CONNECT proxy, with its credentials.
type Proxy struct {
	// Type is socks5 (the node resolves names), socks5h (the proxy resolves names), http, or https.
	Type     string `json:"type"`
	Address  string `json:"address"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Path is one candidate route from the ingress node to an upstream. With FirstNode set, the ingress opens a
// tunnel to it (through LinkProxies) and the relays forward it by Key; otherwise the ingress dials Target
// itself through Egress.
type Path struct {
	Key         string  `json:"key,omitempty"`
	Label       string  `json:"label"`
	FirstNode   string  `json:"firstNode,omitempty"`
	LinkProxies []Proxy `json:"linkProxies,omitempty"`
	Egress      []Proxy `json:"egress,omitempty"`
	Target      string  `json:"target"`
}

// RelayRoute is one tunnel a relay forwards: from one of From to Next (through LinkProxies), or, on the exit
// node, to Target through Egress.
type RelayRoute struct {
	Key         string   `json:"key"`
	From        []string `json:"from"`
	Next        string   `json:"next,omitempty"`
	LinkProxies []Proxy  `json:"linkProxies,omitempty"`
	Egress      []Proxy  `json:"egress,omitempty"`
	Target      string   `json:"target"`
}

// Site is one listener-bound reverse-proxy site.
type Site struct {
	ID            string   `json:"id"`
	ListenAddress string   `json:"listenAddress"`
	ListenPort    int      `json:"listenPort"`
	TLS           bool     `json:"tls,omitempty"`
	Hostnames     []string `json:"hostnames,omitempty"`
	// CertificateID names a shared system server certificate; sites that use one can swap renewed certificate
	// content without restarting.
	CertificateID string          `json:"certificateId,omitempty"`
	Certificate   *KeyPair        `json:"certificate,omitempty"`
	Upstreams     []Upstream      `json:"upstreams"`
	Routes        []routing.Route `json:"routes,omitempty"`
	AccessLog     AccessLog       `json:"accessLog"`
}

// KeyPair is a PEM certificate chain with its private key.
type KeyPair struct {
	CertificatePEM string `json:"certificatePem"`
	PrivateKeyPEM  string `json:"privateKeyPem"`
}

// Upstream is one proxy target with its own transport settings.
type Upstream struct {
	URL                string `json:"url"`
	ProxyURL           string `json:"proxyUrl,omitempty"`
	ProxyType          string `json:"proxyType,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	// RootCAs are PEM CA certificates added to the operating system trust pool.
	RootCAs []string `json:"rootCas,omitempty"`
	// CABundle replaces the operating system pool unless RootCAs is also set, in which case it is added to it.
	CABundle          string   `json:"caBundle,omitempty"`
	ClientCertificate *KeyPair `json:"clientCertificate,omitempty"`
	DialAddress       string   `json:"dialAddress,omitempty"`
	ServerName        string   `json:"serverName,omitempty"`
	// Paths are the candidate routes in priority order; empty means a direct connection (or ProxyURL).
	Paths []Path `json:"paths,omitempty"`
}

// AccessLog controls per-site access logging; an empty AdapterID disables it.
type AccessLog struct {
	AdapterID               string `json:"adapterId,omitempty"`
	IncludeBodies           bool   `json:"includeBodies,omitempty"`
	IncludeSensitiveHeaders bool   `json:"includeSensitiveHeaders,omitempty"`
	MaxBodyBytes            int64  `json:"maxBodyBytes,omitempty"`
}

// RuntimeKey fingerprints everything except the server certificate. Two specs with equal keys can share one
// running site, which then only needs its certificate swapped.
func (s Site) RuntimeKey() string {
	s.Certificate = nil
	return fingerprint(s)
}

// CertificateKey fingerprints the server certificate alone.
func (s Site) CertificateKey() string {
	return fingerprint(s.Certificate)
}

func fingerprint(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		// Every field is a plain JSON value, so marshaling cannot fail.
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
