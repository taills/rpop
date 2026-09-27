package overlay

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
)

const (
	relayHeaderTimeout = 10 * time.Second
	relayPingAfter     = 30 * time.Second
	relayPingTimeout   = 15 * time.Second
)

// relayServer is a node's relay port: an HTTP/2 mutual-TLS listener that forwards CONNECT streams.
type relayServer struct {
	address string
	server  *http.Server
}

func (o *Overlay) startRelay(address string) (*relayServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(o.serveRelay),
		TLSConfig:         o.identity.RelayServerConfig(),
		ReadHeaderTimeout: relayHeaderTimeout,
		ConnContext:       rememberTLSConn,
		ErrorLog:          log.New(io.Discard, "", 0),
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: maxStreamsPerConn, SendPingTimeout: relayPingAfter, PingTimeout: relayPingTimeout,
			MaxReceiveBufferPerStream: streamWindow, MaxReceiveBufferPerConnection: connectionWindow,
		},
	}
	go func() {
		err := server.ServeTLS(tunedListener{listener}, "", "")
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			o.log.Error("relay port stopped", zap.String("address", address), zap.Error(err))
		}
	}()
	o.log.Info("relay port listening", zap.String("address", address))
	return &relayServer{address: address, server: server}, nil
}

// drain stops accepting tunnels and lets open ones finish in the background.
func (r *relayServer) drain() {
	go func() { _ = r.server.Shutdown(context.Background()) }()
}

func (r *relayServer) close() { _ = r.server.Close() }

type tlsConnKey struct{}

// rememberTLSConn keeps the TLS connection in the request context: HTTP/2 leaves Request.TLS nil for CONNECT
// requests, which carry no :scheme, so the peer certificate has to come from the connection.
func rememberTLSConn(ctx context.Context, c net.Conn) context.Context {
	if tlsConn, ok := c.(*tls.Conn); ok {
		return context.WithValue(ctx, tlsConnKey{}, tlsConn)
	}
	return ctx
}

func peerState(r *http.Request) *tls.ConnectionState {
	if r.TLS != nil {
		return r.TLS
	}
	if tlsConn, ok := r.Context().Value(tlsConnKey{}).(*tls.Conn); ok {
		state := tlsConn.ConnectionState()
		return &state
	}
	return nil
}

type tunedListener struct{ net.Listener }

func (l tunedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		tuneConn(conn)
	}
	return conn, err
}

// serveRelay forwards one tunnel along the route the controller rendered for it. A peer can only use routes
// that list it as a previous hop, so no request can steer a relay anywhere the controller did not configure.
func (o *Overlay) serveRelay(w http.ResponseWriter, r *http.Request) {
	peer, ok := o.authorizedPeer(peerState(r))
	if !ok {
		refuse(w, http.StatusForbidden, "peer is not authorized")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == pingPath {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodConnect {
		refuse(w, http.StatusMethodNotAllowed, "only CONNECT is relayed")
		return
	}
	route, ok := (*o.routes.Load())[r.Header.Get(RouteHeader)]
	if !ok || !slices.Contains(route.From, peer) {
		refuse(w, http.StatusForbidden, "route is not allowed from this peer")
		return
	}
	// The tunnel's opener decides whether it logs events (TunnelLogHeader): this relay's own route table cannot
	// tell, because one route can be shared by sites with different access-log settings (P7).
	open := tunnelOpen{tunnelID: r.Header.Get(TunnelIDHeader), logEvents: r.Header.Get(TunnelLogHeader) != ""}
	role := RoleRelay
	if route.Next == "" {
		role = RoleExit
	}
	arrived := time.Now()
	if open.logEvents {
		o.events.record(TunnelEvent{Timestamp: arrived, TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: role, Stage: StageArrived, Peer: peer})
	}
	ctx, cancel := context.WithTimeout(r.Context(), dialTimeout+handshakeTimeout)
	next, err := o.dialNext(ctx, route, open)
	cancel()
	if err != nil {
		if open.logEvents {
			o.events.record(TunnelEvent{
				Timestamp: time.Now(), TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: role, Stage: StageEnded,
				Peer: peer, Duration: time.Since(arrived), Error: err.Error(),
			})
		}
		o.log.Warn("relay could not reach the next hop", zap.String("from", peer), zap.String("next", route.Next), zap.Error(err))
		refuse(w, http.StatusBadGateway, err.Error())
		return
	}
	defer next.Close()
	stop := context.AfterFunc(r.Context(), func() { next.Close() })
	defer stop()
	controller := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return
	}
	established := time.Now()
	if open.logEvents {
		o.events.record(TunnelEvent{Timestamp: established, TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: role, Stage: StageEstablished, Peer: peer, Duration: established.Sub(arrived)})
	}
	var bytesUp, bytesDown atomic.Uint64
	go func() {
		var err error
		if open.logEvents {
			err = copyPooled(next, countingReader{r.Body, &bytesUp})
		} else {
			err = copyPooled(next, r.Body)
		}
		if err == nil {
			_ = closeWrite(next)
		} else {
			next.Close()
		}
	}()
	if open.logEvents {
		_ = copyFlushing(countingWriter{w, &bytesDown}, controller, next)
		o.events.record(TunnelEvent{
			Timestamp: time.Now(), TunnelID: open.tunnelID, NodeID: o.identity.NodeID, Role: role, Stage: StageEnded,
			Peer: peer, Duration: time.Since(established), BytesIn: bytesUp.Load(), BytesOut: bytesDown.Load(),
		})
	} else {
		_ = copyFlushing(w, controller, next)
	}
}

func refuse(w http.ResponseWriter, status int, reason string) {
	w.Header().Set(ErrorHeader, strings.ReplaceAll(reason, "\n", " "))
	w.WriteHeader(status)
}

// authorizedPeer identifies the node on the other side and checks its certificate is of its current registration.
func (o *Overlay) authorizedPeer(state *tls.ConnectionState) (string, bool) {
	id, ok := pki.PeerNodeID(state)
	if !ok {
		return "", false
	}
	peer, known := (*o.peers.Load())[id]
	generation, ok := pki.NodeGeneration(state.PeerCertificates[0])
	return id, known && ok && generation == peer.Generation
}
