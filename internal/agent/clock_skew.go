package agent

import "time"

// clockRoundTimestamps are the four timestamps of one status heartbeat round-trip (D28: NTP-style clock offset
// estimation). sentAt/gotResponseAt are the node's own clock (t0/t3, taken right before sending the request and
// right after its answer arrives); receivedAt/respondedAt are the controller's own clock (t1/t2), echoed back in
// southbound.StatusResponse. The zero value means "no round to compute an offset from yet": the node's very
// first status report, or any report answered by a controller that predates D28 (204 No Content, no
// StatusResponse body to read t1/t2 from).
type clockRoundTimestamps struct {
	sentAt, gotResponseAt   time.Time
	receivedAt, respondedAt time.Time
}

// complete reports whether every one of the round's four timestamps was recorded.
func (t clockRoundTimestamps) complete() bool {
	return !t.sentAt.IsZero() && !t.gotResponseAt.IsZero() && !t.receivedAt.IsZero() && !t.respondedAt.IsZero()
}

// offsetAndRTT applies D28's NTP formulas to a completed round: offset estimates how far this node's clock leads
// the controller's (positive means this node is ahead), assuming the request and response legs of the round trip
// took equal time; rtt is the round-trip time that assumption is made over. ok is false for an incomplete round,
// in which case the other two returns are zero and must not be reported.
//
// The classic NTP offset formula θ = ((T2−T1)+(T3−T4))/2 (T1=sentAt, T2=receivedAt, T3=respondedAt, T4=
// gotResponseAt) computes "T2/T3's clock minus T1/T4's clock" — here, the controller's clock minus this node's.
// D28 defines ClockOffsetMillis the other way around (positive: *this node's* clock is ahead, matching this
// method's own doc comment above, southbound.Status.ClockOffsetMillis's, and the console's — see
// docs/architecture/control-data-plane.md §5's stage 7 review fix note), so this negates θ by swapping each
// subtraction's operands: ((T1−T2)+(T4−T3))/2 = ((sentAt−receivedAt)+(gotResponseAt−respondedAt))/2.
func (t clockRoundTimestamps) offsetAndRTT() (offset, rtt time.Duration, ok bool) {
	if !t.complete() {
		return 0, 0, false
	}
	offset = (t.sentAt.Sub(t.receivedAt) + t.gotResponseAt.Sub(t.respondedAt)) / 2
	rtt = t.gotResponseAt.Sub(t.sentAt) - t.respondedAt.Sub(t.receivedAt)
	return offset, rtt, true
}
