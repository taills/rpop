package dataplane

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/rpop-project/rpop/internal/snapshot"
)

// newTransport builds the dedicated transport of one upstream.
func newTransport(u snapshot.Upstream) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	tlsConfig, err := upstreamTLSConfig(u)
	if err != nil {
		return nil, err
	}
	t.TLSClientConfig = tlsConfig
	if u.ProxyURL != "" {
		if err := useProxy(t, u.ProxyURL); err != nil {
			return nil, err
		}
	} else if u.ProxyType != "" && u.ProxyType != "direct" {
		return nil, fmt.Errorf("proxyUrl required")
	}
	if u.DialAddress != "" && u.ProxyURL == "" {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		target := dialTarget(u.DialAddress, u.URL)
		t.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, target)
		}
	}
	return t, nil
}

func upstreamTLSConfig(u snapshot.Upstream) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.ServerName, InsecureSkipVerify: u.InsecureSkipVerify}
	if len(u.RootCAs) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		for index, certificate := range u.RootCAs {
			if !pool.AppendCertsFromPEM([]byte(certificate)) {
				return nil, fmt.Errorf("invalid system root certificate at index %d", index)
			}
		}
		if u.CABundle != "" && !pool.AppendCertsFromPEM([]byte(u.CABundle)) {
			return nil, fmt.Errorf("invalid upstream CA bundle")
		}
		config.RootCAs = pool
	} else if u.CABundle != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(u.CABundle)) {
			return nil, fmt.Errorf("invalid CA bundle")
		}
		config.RootCAs = pool
	}
	if u.ClientCertificate != nil {
		certificate, err := tls.X509KeyPair([]byte(u.ClientCertificate.CertificatePEM), []byte(u.ClientCertificate.PrivateKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("upstream client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func useProxy(t *http.Transport, rawURL string) error {
	p, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	switch p.Scheme {
	case "http", "https":
		t.Proxy = http.ProxyURL(p)
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if p.User != nil {
			password, _ := p.User.Password()
			auth = &proxy.Auth{User: p.User.Username(), Password: password}
		}
		d, err := proxy.SOCKS5("tcp", p.Host, auth, proxy.Direct)
		if err != nil {
			return err
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return fmt.Errorf("SOCKS5 dialer context unsupported")
		}
		t.DialContext = cd.DialContext
	default:
		return fmt.Errorf("unsupported proxy scheme: %s", p.Scheme)
	}
	return nil
}

// dialTarget adds the upstream URL's default port to a dial address given without one.
func dialTarget(dialAddress, upstreamURL string) string {
	if strings.Contains(dialAddress, ":") {
		return dialAddress
	}
	port := "80"
	if strings.HasPrefix(strings.ToLower(upstreamURL), "https://") {
		port = "443"
	}
	return net.JoinHostPort(dialAddress, port)
}
