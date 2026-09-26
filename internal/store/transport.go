package store

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"golang.org/x/net/proxy"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func BuildTransport(ctx context.Context, s *Store, siteID string, u Upstream, systemRootCertificates []string) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.ServerName, InsecureSkipVerify: u.InsecureSkipVerify}
	if len(systemRootCertificates) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		for index, certificate := range systemRootCertificates {
			if !pool.AppendCertsFromPEM([]byte(certificate)) {
				return nil, fmt.Errorf("invalid system root certificate at index %d", index)
			}
		}
		if u.CABundle != "" && !pool.AppendCertsFromPEM([]byte(u.CABundle)) {
			return nil, fmt.Errorf("invalid upstream CA bundle")
		}
		t.TLSClientConfig.RootCAs = pool
	} else if u.CABundle != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(u.CABundle)) {
			return nil, fmt.Errorf("invalid CA bundle")
		}
		t.TLSClientConfig.RootCAs = pool
	}
	if u.ClientCertSecret != "" || u.ClientKeySecret != "" {
		if u.ClientCertSecret == "" || u.ClientKeySecret == "" {
			return nil, fmt.Errorf("both upstream client certificate and key are required")
		}
		certPEM, e := s.Secret(ctx, siteID, u.ClientCertSecret)
		if e != nil {
			return nil, e
		}
		keyPEM, e := s.Secret(ctx, siteID, u.ClientKeySecret)
		if e != nil {
			return nil, e
		}
		cert, e := tls.X509KeyPair(certPEM, keyPEM)
		if e != nil {
			return nil, e
		}
		t.TLSClientConfig.Certificates = []tls.Certificate{cert}
	}
	if u.ProxyURL != "" {
		p, e := url.Parse(u.ProxyURL)
		if e != nil {
			return nil, e
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
			d, e := proxy.SOCKS5("tcp", p.Host, auth, proxy.Direct)
			if e != nil {
				return nil, e
			}
			cd, ok := d.(proxy.ContextDialer)
			if !ok {
				return nil, fmt.Errorf("SOCKS5 dialer context unsupported")
			}
			t.DialContext = cd.DialContext
		default:
			return nil, fmt.Errorf("unsupported proxy scheme: %s", p.Scheme)
		}
	} else if u.ProxyType != "" && u.ProxyType != "direct" {
		return nil, fmt.Errorf("proxyUrl required")
	}
	if u.DialAddress != "" && u.ProxyURL == "" {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			target := u.DialAddress
			if !strings.Contains(target, ":") {
				port := "80"
				if strings.HasPrefix(strings.ToLower(u.URL), "https://") {
					port = "443"
				}
				target = net.JoinHostPort(target, port)
			}
			return d.DialContext(ctx, network, target)
		}
	}
	return t, nil
}
