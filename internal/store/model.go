package store

import "github.com/rpop-project/rpop/internal/routing"

type Upstream struct {
	URL                 string   `json:"url"`
	ProxyURL            string   `json:"proxyUrl,omitempty"`
	ProxyType           string   `json:"proxyType,omitempty"`
	InsecureSkipVerify  bool     `json:"insecureSkipVerify,omitempty"`
	CABundle            string   `json:"caBundle,omitempty"`
	RootCertificateIDs  []string `json:"rootCertificateIds,omitempty"`
	ClientCertificateID string   `json:"clientCertificateId,omitempty"`
	ClientCertSecret    string   `json:"clientCertSecret,omitempty"`
	ClientKeySecret     string   `json:"clientKeySecret,omitempty"`
	DialAddress         string   `json:"dialAddress,omitempty"`
	ServerName          string   `json:"serverName,omitempty"`
	// Paths route the upstream connection through nodes and named proxies, in priority order: the first path
	// that connects is used. Via is shorthand for a single path.
	Paths []UpstreamPath `json:"paths,omitempty"`
	Via   []Hop          `json:"via,omitempty"`
	// Failover overrides the global D19/D30 path degradation tuning for this upstream; nil keeps every default.
	// It is a sibling of Paths, not a per-path field: cooldown and probing are a per-upstream trade-off.
	Failover *UpstreamFailover `json:"failover,omitempty"`
}

// UpstreamFailover overrides the node-wide defaults (internal/dataplane's pathFailoverConfig) for one upstream's
// candidate paths; a nil field keeps that setting's default. See internal/control/paths.go's validateFailover
// for the accepted ranges.
type UpstreamFailover struct {
	// DialTimeoutMs bounds connecting along one candidate path; 0 keeps the node default (10s).
	DialTimeoutMs int64 `json:"dialTimeoutMs,omitempty"`
	// MinCooldownMs/MaxCooldownMs bound the exponential cooldown a path serves after a connection failure; 0
	// keeps the node defaults (1s/1min).
	MinCooldownMs int64 `json:"minCooldownMs,omitempty"`
	MaxCooldownMs int64 `json:"maxCooldownMs,omitempty"`
	// ActiveProbe overrides the global D19 active-probe switch for this upstream; nil keeps the global default.
	ActiveProbe *bool `json:"activeProbe,omitempty"`
}

// UpstreamPath is one candidate route from the site's node to the upstream. An empty Via connects directly.
type UpstreamPath struct {
	Via []Hop `json:"via"`
}

// Hop is one step of a path: a node that relays the connection, or a named proxy it passes through.
type Hop struct {
	Node  string `json:"node,omitempty"`
	Proxy string `json:"proxy,omitempty"`
}

// Route and HeaderMatch are defined by the routing package, which the controller and data plane share.
type (
	Route       = routing.Route
	HeaderMatch = routing.HeaderMatch
)

type AccessLogConfig struct {
	AdapterID               string `json:"adapterId,omitempty"`
	IncludeBodies           bool   `json:"includeBodies,omitempty"`
	IncludeSensitiveHeaders bool   `json:"includeSensitiveHeaders,omitempty"`
	MaxBodyBytes            int64  `json:"maxBodyBytes,omitempty"`
}
type Config struct {
	// Nodes places the site on data-plane nodes; empty means the node embedded in the controller.
	Nodes             []string        `json:"nodes,omitempty"`
	Hostnames         []string        `json:"hostnames,omitempty"`
	ListenAddress     string          `json:"listenAddress"`
	ListenPort        int             `json:"listenPort"`
	TLS               bool            `json:"tls,omitempty"`
	CertificateID     string          `json:"certificateId,omitempty"`
	CertificateSecret string          `json:"certificateSecret,omitempty"`
	PrivateKeySecret  string          `json:"privateKeySecret,omitempty"`
	Upstreams         []Upstream      `json:"upstreams"`
	Routes            []Route         `json:"routes,omitempty"`
	AccessLog         AccessLogConfig `json:"accessLog,omitempty"`
}
type Site struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Config    Config `json:"config"`
	AutoStart bool   `json:"autoStart"`
	Running   bool   `json:"running"`
	UpdatedAt string `json:"updatedAt"`
}
