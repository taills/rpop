package store

type Upstream struct {
	URL                string `json:"url" yaml:"url"`
	ProxyURL           string `json:"proxyUrl,omitempty" yaml:"proxyUrl,omitempty"`
	ProxyType          string `json:"proxyType,omitempty" yaml:"proxyType,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty" yaml:"insecureSkipVerify,omitempty"`
	CABundle           string `json:"caBundle,omitempty" yaml:"caBundle,omitempty"`
	ClientCertSecret   string `json:"clientCertSecret,omitempty" yaml:"clientCertSecret,omitempty"`
	ClientKeySecret    string `json:"clientKeySecret,omitempty" yaml:"clientKeySecret,omitempty"`
	DialAddress        string `json:"dialAddress,omitempty" yaml:"dialAddress,omitempty"`
	ServerName         string `json:"serverName,omitempty" yaml:"serverName,omitempty"`
}
type AccessLogConfig struct {
	IncludeBodies           bool  `json:"includeBodies,omitempty" yaml:"includeBodies,omitempty"`
	IncludeSensitiveHeaders bool  `json:"includeSensitiveHeaders,omitempty" yaml:"includeSensitiveHeaders,omitempty"`
	MaxBodyBytes            int64 `json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
}
type Config struct {
	Hostnames         []string        `json:"hostnames,omitempty" yaml:"hostnames,omitempty"`
	ListenAddress     string          `json:"listenAddress" yaml:"listenAddress"`
	ListenPort        int             `json:"listenPort" yaml:"listenPort"`
	TLS               bool            `json:"tls,omitempty" yaml:"tls,omitempty"`
	CertificateSecret string          `json:"certificateSecret,omitempty" yaml:"certificateSecret,omitempty"`
	PrivateKeySecret  string          `json:"privateKeySecret,omitempty" yaml:"privateKeySecret,omitempty"`
	Upstreams         []Upstream      `json:"upstreams" yaml:"upstreams"`
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
