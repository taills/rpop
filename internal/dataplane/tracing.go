package dataplane

import (
	"net"
	"net/http"
	"strings"

	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/traceid"
)

// TrackIDHeader is the request header the ingress site sets on every request: a per-request correlation ID
// that follows the request to the upstream and, when access logging is on, into the access log (D22).
const TrackIDHeader = "Rpop-Track-Id"

// assignTrackID overwrites r's track ID header with a freshly minted one and returns it. A client-supplied
// value is never trusted or forwarded: accepting it would let a client forge another request's trace or plant
// misleading correlations in the log destination, and generation is cheap enough (no syscalls, P8) that there
// is no reason to special-case the common case where the client sent none.
func assignTrackID(r *http.Request) string {
	id := traceid.New()
	r.Header.Set(TrackIDHeader, id)
	return id
}

// accessLoggingEnabled reports whether a site's access log settings name a destination adapter.
func accessLoggingEnabled(settings snapshot.AccessLog) bool {
	return strings.TrimSpace(settings.AdapterID) != ""
}

// tunnelIDer is implemented by the connections the overlay package hands back for tunnels that cross nodes.
type tunnelIDer interface{ TunnelID() string }

// netConnUnwrapper matches (*tls.Conn).NetConn: an upstream reached over TLS terminates it end-to-end through
// the tunnel, so the connection httptrace reports is a *tls.Conn wrapping the tunnel, not the tunnel itself.
type netConnUnwrapper interface{ NetConn() net.Conn }

// tunnelIDFromConn returns the tunnel ID of the connection a request went out on, or "" when it did not cross
// another node (a direct or same-node upstream has no tunnel).
func tunnelIDFromConn(conn net.Conn) string {
	if unwrapper, ok := conn.(netConnUnwrapper); ok {
		conn = unwrapper.NetConn()
	}
	if withID, ok := conn.(tunnelIDer); ok {
		return withID.TunnelID()
	}
	return ""
}
