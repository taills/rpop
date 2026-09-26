package store

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
}

// HeaderMatch is one request-header condition of a Route, following Caddy's header matcher:
// each value may be exact, "prefix*", "*suffix" or "*substring*"; values are ORed.
// No values means the header must be present; Absent means it must not be present.
type HeaderMatch struct {
	Name   string   `json:"name" yaml:"name"`
	Values []string `json:"values,omitempty" yaml:"values,omitempty"`
	Absent bool     `json:"absent,omitempty" yaml:"absent,omitempty"`
}

// Route sends matching requests to Upstreams[Upstream]. Path is exact unless it ends in "*"
// (prefix match) and is compared case-insensitively; an empty Path matches every path.
// All header conditions must hold. StripPrefix removes the matched path prefix before proxying.
type Route struct {
	Path        string        `json:"path,omitempty" yaml:"path,omitempty"`
	Headers     []HeaderMatch `json:"headers,omitempty" yaml:"headers,omitempty"`
	StripPrefix bool          `json:"stripPrefix,omitempty" yaml:"stripPrefix,omitempty"`
	Upstream    int           `json:"upstream" yaml:"upstream"`
}
type AccessLogConfig struct {
	AdapterID               string `json:"adapterId,omitempty" yaml:"adapterId,omitempty"`
	IncludeBodies           bool   `json:"includeBodies,omitempty" yaml:"includeBodies,omitempty"`
	IncludeSensitiveHeaders bool   `json:"includeSensitiveHeaders,omitempty" yaml:"includeSensitiveHeaders,omitempty"`
	MaxBodyBytes            int64  `json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
}
type Config struct {
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
