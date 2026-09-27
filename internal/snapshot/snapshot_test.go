package snapshot

import (
	"encoding/json"
	"testing"
)

// TestUpstreamFailoverJSONRoundTrip covers the wire shape a snapshot's optional D19/D30 override travels in:
// nil stays absent from the JSON (never a null field, see the omitempty tags), and every set field, including
// the ActiveProbe tri-state, survives a marshal/unmarshal round trip unchanged.
func TestUpstreamFailoverJSONRoundTrip(t *testing.T) {
	activeProbe := false
	upstream := Upstream{URL: "https://example.com", Failover: &UpstreamFailover{
		DialTimeoutMs: 5000, MinCooldownMs: 200, MaxCooldownMs: 30_000, ActiveProbe: &activeProbe,
	}}
	data, err := json.Marshal(upstream)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Upstream
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	f := decoded.Failover
	if f == nil || f.DialTimeoutMs != 5000 || f.MinCooldownMs != 200 || f.MaxCooldownMs != 30_000 || f.ActiveProbe == nil || *f.ActiveProbe != false {
		t.Fatalf("round-tripped failover = %#v, want {5000 200 30000 false}", f)
	}

	bare, err := json.Marshal(Upstream{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var decodedBare Upstream
	if err := json.Unmarshal(bare, &decodedBare); err != nil {
		t.Fatal(err)
	}
	if decodedBare.Failover != nil {
		t.Fatalf("failover = %#v, want nil for an upstream with no override", decodedBare.Failover)
	}
}

// TestSiteRuntimeKeyChangesWithFailover ensures a site whose only change is its Failover override still gets a
// fresh RuntimeKey, so the data plane rebuilds its transports (and thus applies the new dial timeout/cooldown
// bounds) instead of assuming nothing changed.
func TestSiteRuntimeKeyChangesWithFailover(t *testing.T) {
	base := Site{ID: "s", Upstreams: []Upstream{{URL: "https://example.com"}}}
	withOverride := Site{ID: "s", Upstreams: []Upstream{{URL: "https://example.com", Failover: &UpstreamFailover{DialTimeoutMs: 5000}}}}
	if base.RuntimeKey() == withOverride.RuntimeKey() {
		t.Fatal("RuntimeKey did not change when Failover was added")
	}
	widerCooldown := Site{ID: "s", Upstreams: []Upstream{{URL: "https://example.com", Failover: &UpstreamFailover{DialTimeoutMs: 6000}}}}
	if withOverride.RuntimeKey() == widerCooldown.RuntimeKey() {
		t.Fatal("RuntimeKey did not change when a Failover field's value changed")
	}
}
