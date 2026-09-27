// Package overlay carries upstream connections across nodes: persistent HTTP/2 mutual-TLS links between nodes,
// tunnels multiplexed on them as CONNECT streams, relays that forward tunnels by controller-rendered routes,
// and dialers that pass through chains of SOCKS5 and HTTP CONNECT proxies.
package overlay

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"

	"github.com/rpop-project/rpop/internal/snapshot"
)

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 10 * time.Second
	tcpKeepAlive     = 30 * time.Second
)

var tcpDialer = &net.Dialer{Timeout: dialTimeout, KeepAlive: tcpKeepAlive, Control: tuneSocket}

// ProxyChainLabels renders a proxy chain for status reporting, as "type://address" per hop in dial order; it
// never includes credentials (see throughProxy's own error formatting below, which only ever wraps addresses
// and response status text). nil for a direct link with no proxies, so callers can compare it against another
// chain's labels with slices.Equal without normalizing nil against an empty slice.
func ProxyChainLabels(proxies []snapshot.Proxy) []string {
	if len(proxies) == 0 {
		return nil
	}
	labels := make([]string, len(proxies))
	for i, p := range proxies {
		labels[i] = p.Type + "://" + p.Address
	}
	return labels
}

// DialChain connects to target through proxies in order; with no proxies it dials target directly.
func DialChain(ctx context.Context, proxies []snapshot.Proxy, target string) (net.Conn, error) {
	if len(proxies) == 0 {
		return tcpDialer.DialContext(ctx, "tcp", target)
	}
	conn, err := tcpDialer.DialContext(ctx, "tcp", proxies[0].Address)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxies[0].Address, err)
	}
	for index, p := range proxies {
		next := target
		if index+1 < len(proxies) {
			next = proxies[index+1].Address
		}
		if conn, err = throughProxy(ctx, conn, p, next); err != nil {
			return nil, fmt.Errorf("proxy %s: %w", p.Address, err)
		}
	}
	return conn, nil
}

// throughProxy asks the proxy at the far end of conn to connect to next. It closes conn on failure.
func throughProxy(ctx context.Context, conn net.Conn, p snapshot.Proxy, next string) (net.Conn, error) {
	switch p.Type {
	case "socks5", "socks5h":
		return socksConnect(ctx, conn, p, next)
	case "https":
		host, _, _ := net.SplitHostPort(p.Address)
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return httpConnect(ctx, tlsConn, p, next)
	case "http":
		return httpConnect(ctx, conn, p, next)
	default:
		conn.Close()
		return nil, fmt.Errorf("unsupported proxy type %q", p.Type)
	}
}

// connDialer hands a SOCKS client the connection it should speak over.
type connDialer struct{ conn net.Conn }

func (d connDialer) Dial(string, string) (net.Conn, error) { return d.conn, nil }
func (d connDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return d.conn, nil
}

func socksConnect(ctx context.Context, conn net.Conn, p snapshot.Proxy, next string) (net.Conn, error) {
	if p.Type == "socks5" {
		resolved, err := resolveLocally(ctx, next)
		if err != nil {
			conn.Close()
			return nil, err
		}
		next = resolved
	}
	var auth *proxy.Auth
	if p.Username != "" {
		auth = &proxy.Auth{User: p.Username, Password: p.Password}
	}
	dialer, err := proxy.SOCKS5("tcp", p.Address, auth, connDialer{conn})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", next); err != nil {
		conn.Close()
		return nil, err
	}
	// The SOCKS client wraps conn only to record the bound address, and the wrapper hides CloseWrite, which
	// relays need to pass a half-close on.
	return conn, nil
}

// resolveLocally turns host:port into ip:port, for SOCKS5 proxies that should not resolve names.
func resolveLocally(ctx context.Context, address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	if net.ParseIP(host) != nil {
		return address, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no address for %s", host)
	}
	return net.JoinHostPort(addrs[0].IP.String(), port), nil
}

func httpConnect(ctx context.Context, conn net.Conn, p snapshot.Proxy, next string) (net.Conn, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(handshakeTimeout)
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: next}, Host: next, Header: make(http.Header)}
	if p.Username != "" {
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.Username+":"+p.Password)))
	}
	if err := request.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		conn.Close()
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("CONNECT %s: %s", next, response.Status)
	}
	if !stop() && ctx.Err() != nil {
		conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

// bufferedConn returns bytes a proxy sent right after its CONNECT response before reading the connection.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

// CloseWrite half-closes the connection when the underlying connection supports it.
func (c *bufferedConn) CloseWrite() error {
	return closeWrite(c.Conn)
}

func closeWrite(conn net.Conn) error {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.ErrUnsupported
}
