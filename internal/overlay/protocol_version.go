package overlay

import (
	"strconv"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

// ProtocolVersionHeader carries the rpop wire protocol version on every overlay CONNECT tunnel handshake (D27):
// the dialing node sets it on the CONNECT request and the accepting relay echoes it on the response, so either
// side can notice a mismatch from a single round trip. It is the same header name southbound uses on the
// control channel (see southbound.ProtocolVersionHeader) — defined here, not imported there, because
// southbound.Status already embeds overlay.LinkStatus, so overlay cannot import southbound back without a
// cycle; southbound instead aliases this package's constants (see southbound.ProtocolVersion's doc comment).
const ProtocolVersionHeader = "Rpop-Protocol-Version"

// ProtocolVersion is the protocol version this build of rpop speaks; bump it only for a breaking change (field
// semantics changed, a new field made required, or an existing enum value's meaning changed) — a purely additive
// optional field, the norm through stages 5 and 6, never requires it. A mismatch on an overlay tunnel handshake
// is only ever logged (see checkTunnelProtocolVersion): the overlay favors tunnel availability over version
// agreement, unlike the southbound control channel, which can refuse a request outright (D27).
const ProtocolVersion = 1

// checkTunnelProtocolVersion records a peer's reported protocol version from a CONNECT handshake, in either
// direction (dialing a link or accepting one on the relay port), when it differs from this build's
// ProtocolVersion. It never causes the tunnel to fail: overlay/relay CONNECT handshakes always proceed
// regardless of the result (D27). reported is what the other side's Rpop-Protocol-Version header carried; an
// empty string (a peer that predates D27, or a relay's ping request, which carries no such header) is treated
// as "unknown", not as a mismatch, since warning on every request to an old, pre-D27 peer would defeat the point
// of logging only once. Logging is deduplicated per peer (protocolWarned) so a peer stuck on a mismatched
// version logs once for the life of this Overlay, not once per tunnel; protocolMismatches keeps a running total
// alongside that for anything that wants to poll it instead of scraping logs.
func (o *Overlay) checkTunnelProtocolVersion(peer, reported string) {
	if reported == "" || reported == strconv.Itoa(ProtocolVersion) {
		return
	}
	o.protocolMismatches.Add(1)
	if _, already := o.protocolWarned.LoadOrStore(peer, struct{}{}); already {
		return
	}
	o.log.Warn("overlay peer reported a different protocol version; the tunnel is not rejected over this",
		zap.String("peer", peer), zap.String("peer_version", reported), zap.Int("version", ProtocolVersion))
}

// protocolVersionState is embedded in Overlay to keep checkTunnelProtocolVersion's bookkeeping next to the
// fields it owns; see New for its zero-value initialization (sync.Map and atomic.Uint64 need none).
type protocolVersionState struct {
	protocolWarned     sync.Map
	protocolMismatches atomic.Uint64
}
