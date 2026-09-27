package store

import "github.com/rpop-project/rpop/internal/routing"

type Upstream struct {
	URL                 string   `json:"url" yaml:"url"`
	ProxyURL            string   `json:"proxyUrl,omitempty" yaml:"proxyUrl,omitempty"`
	ProxyType           string   `json:"proxyType,omitempty" yaml:"proxyType,omitempty"`
	InsecureSkipVerify  bool     `json:"insecureSkipVerify,omitempty" yaml:"insecureSkipVerify,omitempty"`
	CABundle            string   `json:"caBundle,omitempty" yaml:"caBundle,omitempty"`
	RootCertificateIDs  []string `json:"rootCertificateIds,omitempty" yaml:"rootCertificateIds,omitempty"`
	ClientCertificateID string   `json:"clientCertificateId,omitempty" yaml:"clientCertificateId,omitempty"`
	ClientCertSecret    string   `json:"clientCertSecret,omitempty" yaml:"clientCertSecret,omitempty"`
	ClientKeySecret     string   `json:"clientKeySecret,omitempty" yaml:"clientKeySecret,omitempty"`
	DialAddress         string   `json:"dialAddress,omitempty" yaml:"dialAddress,omitempty"`
	ServerName          string   `json:"serverName,omitempty" yaml:"serverName,omitempty"`
	// Paths route the upstream connection through nodes and named proxies, in priority order: the first path
	// that connects is used. Via is shorthand for a single path.
	Paths []UpstreamPath `json:"paths,omitempty" yaml:"paths,omitempty"`
	Via   []Hop          `json:"via,omitempty" yaml:"via,omitempty"`
}

// UpstreamPath is one candidate route from the site's node to the upstream. An empty Via connects directly.
type UpstreamPath struct {
	Via []Hop `json:"via" yaml:"via"`
}

// Hop is one step of a path: a node that relays the connection, or a named proxy it passes through.
type Hop struct {
	Node  string `json:"node,omitempty" yaml:"node,omitempty"`
	Proxy string `json:"proxy,omitempty" yaml:"proxy,omitempty"`
}

// Route and HeaderMatch are defined by the routing package, which the controller and data plane share.
type (
	Route       = routing.Route
	HeaderMatch = routing.HeaderMatch
)

type AccessLogConfig struct {
	AdapterID               string `json:"adapterId,omitempty" yaml:"adapterId,omitempty"`
	IncludeBodies           bool   `json:"includeBodies,omitempty" yaml:"includeBodies,omitempty"`
	IncludeSensitiveHeaders bool   `json:"includeSensitiveHeaders,omitempty" yaml:"includeSensitiveHeaders,omitempty"`
	MaxBodyBytes            int64  `json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
}
type Config struct {
	// Nodes places the site on data-plane nodes; empty means the node embedded in the controller.
	Nodes             []string        `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	Hostnames         []string        `json:"hostnames,omitempty" yaml:"hostnames,omitempty"`
	ListenAddress     string          `json:"listenAddress" yaml:"listenAddress"`
	ListenPort        int             `json:"listenPort" yaml:"listenPort"`
	TLS               bool            `json:"tls,omitempty" yaml:"tls,omitempty"`
	CertificateID     string          `json:"certificateId,omitempty" yaml:"certificateId,omitempty"`
	CertificateSecret string          `json:"certificateSecret,omitempty" yaml:"certificateSecret,omitempty"`
	PrivateKeySecret  string          `json:"privateKeySecret,omitempty" yaml:"privateKeySecret,omitempty"`
	Upstreams         []Upstream      `json:"upstreams" yaml:"upstreams"`
	Routes            []Route         `json:"routes,omitempty" yaml:"routes,omitempty"`
	AccessLog         AccessLogConfig `json:"accessLog,omitempty" yaml:"accessLog,omitempty"`
}
type Site struct {
	ID        string `json:"id" yaml:"id"`
	Name      string `json:"name" yaml:"name"`
	Config    Config `json:"config" yaml:"config"`
	AutoStart bool   `json:"autoStart" yaml:"autoStart"`
	Running   bool   `json:"running" yaml:"-"`
	UpdatedAt string `json:"updatedAt" yaml:"-"`
}
