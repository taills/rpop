// Package sharedport lets several independent servers of one rpop process share a single TCP port, so a machine
// that allows only one inbound port can still run the console, a site's data-plane listener, and a node's relay
// port at once, telling them apart by Host (plaintext) or SNI (TLS).
//
// The package is a plain network-level multiplexer: it knows nothing about sites, snapshots, overlays, or the
// control plane. A Registry binds each distinct address exactly once (see NormalizeAddress for what "distinct"
// means) and, per connection, peeks the first byte to decide whether it is TLS (0x16) or plaintext, then routes
// it:
//
//   - A TLS connection is matched by SNI against the exact names of registered TLS owners (PutTLSOwner, e.g. a
//     node's relay port) first, then against the hostnames of registered TLS sites (PutSite), then, if
//     registered, against the address's default TLS owner (PutDefaultTLSOwner, e.g. southbound: a node whose
//     -controller URL names an IP, or one too old to pin its SNI, never sends one at all); the matching owner's
//     or the shared TLS-sites server's own tls.Config drives the handshake, so an owner's mTLS requirement never
//     leaks onto a site's connection or vice versa. No match fails the handshake.
//   - A plaintext connection is routed by the Host header: registered plaintext sites are tried first (with the
//     same hostname/wildcard rules as before this package existed), then the plaintext owner (PutPlaintextOwner,
//     e.g. the console), then a 404.
//
// A TLS owner (exact or default) drives its own *http.Server against the net.Listener PutTLSOwner/
// PutDefaultTLSOwner returns, so it keeps full control of HTTP/2 settings, ConnContext, and its handler; sites of
// either kind share one *http.Server per port and per encoding, mirroring how multiple sites already shared one
// listener before this package existed.
//
// The registry keeps a bound address open only as long as something is registered on it: the last owner or site
// to leave an address closes its listener synchronously, so a new registration at that address (even a
// completely unrelated one) can bind immediately afterward.
//
// Every accepted connection costs exactly one extra byte peek and, for TLS, one extra handshake dispatch; the
// registration tables themselves are copy-on-write and swapped with atomic.Pointer, so looking one up while
// routing a connection never takes a lock.
package sharedport
