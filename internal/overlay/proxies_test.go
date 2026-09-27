package overlay

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
)

// testSocks is a minimal SOCKS5 server with optional username/password authentication.
type testSocks struct {
	listener       net.Listener
	user, password string
	mu             sync.Mutex
	requested      []string
}

func startSocks(t *testing.T, user, password string) *testSocks {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testSocks{listener: listener, user: user, password: password}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *testSocks) addr() string { return s.listener.Addr().String() }

func (s *testSocks) targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requested...)
}

func (s *testSocks) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(r, methods); err != nil {
		return
	}
	if s.user == "" {
		conn.Write([]byte{5, 0})
	} else {
		conn.Write([]byte{5, 2})
		version, _ := r.ReadByte()
		userLen, _ := r.ReadByte()
		user := make([]byte, userLen)
		io.ReadFull(r, user)
		passLen, _ := r.ReadByte()
		pass := make([]byte, passLen)
		io.ReadFull(r, pass)
		if version != 1 || string(user) != s.user || string(pass) != s.password {
			conn.Write([]byte{1, 1})
			return
		}
		conn.Write([]byte{1, 0})
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(r, request); err != nil || request[1] != 1 {
		return
	}
	var host string
	switch request[3] {
	case 1:
		ip := make([]byte, 4)
		io.ReadFull(r, ip)
		host = net.IP(ip).String()
	case 3:
		n, _ := r.ReadByte()
		name := make([]byte, n)
		io.ReadFull(r, name)
		host = string(name)
	case 4:
		ip := make([]byte, 16)
		io.ReadFull(r, ip)
		host = net.IP(ip).String()
	}
	portBytes := make([]byte, 2)
	io.ReadFull(r, portBytes)
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes))))
	s.mu.Lock()
	s.requested = append(s.requested, target)
	s.mu.Unlock()
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	pipe(conn, r, upstream)
}

// pipe copies both ways and half-closes each side when its source ends.
func pipe(conn net.Conn, fromConn io.Reader, upstream net.Conn) {
	done := make(chan struct{})
	go func() {
		io.Copy(upstream, fromConn)
		_ = closeWrite(upstream)
		close(done)
	}()
	io.Copy(conn, upstream)
	_ = closeWrite(conn)
	<-done
}

// startHTTPProxy runs an HTTP CONNECT proxy that requires the given credentials when user is not empty.
func startHTTPProxy(t *testing.T, user, password string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		if user != "" && r.Header.Get("Proxy-Authorization") != want {
			http.Error(w, "auth", http.StatusProxyAuthRequired)
			return
		}
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		pipe(conn, buffered, upstream)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().String()
}
