package overlay

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Headers that carry a tunnel's tracing identity across the CONNECT stream that opens it. They travel only
// between nodes over their mutually authenticated relay links (see authorizedPeer), never through an
// unauthenticated client, so a relay can trust them without checking anything else.
const (
	// TunnelIDHeader names the tunnel a CONNECT stream opens, minted once by the node that opens it.
	TunnelIDHeader = "Rpop-Tunnel-Id"
	// TunnelLogHeader is set when the site that requested the tunnel has access logging enabled. Relays cannot
	// tell this from their own route table (P7): one relay route can be shared by many sites (see routeKey in
	// internal/control/paths.go), so whether to log is a per-tunnel decision the opener carries on the request,
	// not a static property of the route.
	TunnelLogHeader = "Rpop-Tunnel-Log"
)

// Hop roles recorded on a TunnelEvent.
const (
	RoleEntry = "entry"
	RoleRelay = "relay"
	RoleExit  = "exit"
)

// Lifecycle stages recorded on a TunnelEvent.
const (
	// StageArrived is a dial request arriving at a hop: the local intent to open a tunnel on the entry node, or
	// a CONNECT stream reaching a relay or exit node.
	StageArrived = "arrived"
	// StageEstablished is the hop successfully connecting onward: the CONNECT succeeding on the entry node, or
	// the relay/exit successfully dialing its next hop or the upstream target.
	StageEstablished = "established"
	// StageEnded is the tunnel closing at this hop.
	StageEnded = "ended"
)

// TunnelEvent is one lifecycle point of a tunnel that crosses nodes, produced only when the site that opened it
// has access logging enabled (P7 and D22). A relay only ever sees ciphertext, so this is the finest granularity
// it can log: connection-level, not per-request. BytesIn/BytesOut on a StageEnded event are best-effort counts
// taken when this hop stops forwarding; they do not wait for the other direction to finish draining, so
// forwarding is never held up for the sake of a log (P8).
type TunnelEvent struct {
	Timestamp time.Time     `json:"timestamp"`
	TunnelID  string        `json:"tunnelId"`
	NodeID    string        `json:"nodeId"`
	Role      string        `json:"role"`
	Stage     string        `json:"stage"`
	Peer      string        `json:"peer,omitempty"`
	BytesIn   uint64        `json:"bytesIn,omitempty"`
	BytesOut  uint64        `json:"bytesOut,omitempty"`
	Duration  time.Duration `json:"duration,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// TunnelEventSink receives tunnel lifecycle events for eventual delivery to the controller, through the node's
// local spool (a later stage). Overlay never blocks forwarding on it (P8); SetTunnelEventSink installs one.
type TunnelEventSink interface {
	RecordTunnelEvent(TunnelEvent)
}

const tunnelEventQueueLength = 256

type sinkHolder struct{ sink TunnelEventSink }

// eventQueue moves tunnel events off the forwarding path: producers never block, and a full queue drops events,
// counting the drops rather than growing without bound (P8). Its shape mirrors dataplane's access log queue.
type eventQueue struct {
	events  chan TunnelEvent
	dropped atomic.Uint64
	sink    atomic.Pointer[sinkHolder]
	log     *zap.Logger
}

func newEventQueue(log *zap.Logger) *eventQueue {
	q := &eventQueue{events: make(chan TunnelEvent, tunnelEventQueueLength), log: log}
	go q.loop()
	return q
}

func (q *eventQueue) setSink(sink TunnelEventSink) { q.sink.Store(&sinkHolder{sink: sink}) }

// record enqueues an event without blocking; a full queue drops it and counts the drop.
func (q *eventQueue) record(event TunnelEvent) {
	select {
	case q.events <- event:
	default:
		q.dropped.Add(1)
	}
}

func (q *eventQueue) loop() {
	for event := range q.events {
		if holder := q.sink.Load(); holder != nil && holder.sink != nil {
			holder.sink.RecordTunnelEvent(event)
		} else {
			q.log.Info("tunnel event", tunnelEventFields(event)...)
		}
	}
}

func tunnelEventFields(e TunnelEvent) []zap.Field {
	fields := []zap.Field{
		zap.Time("timestamp", e.Timestamp), zap.String("tunnel_id", e.TunnelID), zap.String("node_id", e.NodeID),
		zap.String("role", e.Role), zap.String("stage", e.Stage), zap.String("peer", e.Peer),
	}
	if e.BytesIn > 0 || e.BytesOut > 0 {
		fields = append(fields, zap.Uint64("bytes_in", e.BytesIn), zap.Uint64("bytes_out", e.BytesOut))
	}
	if e.Duration > 0 {
		fields = append(fields, zap.Duration("duration", e.Duration))
	}
	if e.Error != "" {
		fields = append(fields, zap.String("error", e.Error))
	}
	return fields
}

// SetTunnelEventSink directs tunnel lifecycle events to sink; until called, events go to the logger.
func (o *Overlay) SetTunnelEventSink(sink TunnelEventSink) { o.events.setSink(sink) }

// DroppedTunnelEvents reports how many tunnel events this node's overlay has discarded because its local queue
// was full.
func (o *Overlay) DroppedTunnelEvents() uint64 { return o.events.dropped.Load() }

type tunnelLoggingKey struct{}

// WithTunnelLogging marks ctx so a dial started through it also records tunnel lifecycle events. The data plane
// sets this once per site, from the site's own access-log setting, before calling DialPath: only the site
// knows whether it wants tunnel events, and a relay cannot derive that from its route table (see
// TunnelLogHeader), so the decision has to travel in from the caller.
func WithTunnelLogging(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, tunnelLoggingKey{}, enabled)
}

func tunnelLoggingEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(tunnelLoggingKey{}).(bool)
	return enabled
}
