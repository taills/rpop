package overlay

import (
	"context"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

func TestProxyChainLabelsNamesEachHopWithoutCredentials(t *testing.T) {
	tests := []struct {
		name    string
		proxies []snapshot.Proxy
		want    []string
	}{
		{name: "no proxies", proxies: nil, want: nil},
		{name: "one hop", proxies: []snapshot.Proxy{{Type: "socks5", Address: "10.0.0.1:1080", Username: "u", Password: "p"}},
			want: []string{"socks5://10.0.0.1:1080"}},
		{name: "chain", proxies: []snapshot.Proxy{{Type: "https", Address: "a:443"}, {Type: "socks5h", Address: "b:1080", Username: "alice", Password: "super-secret"}},
			want: []string{"https://a:443", "socks5h://b:1080"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProxyChainLabels(tt.proxies)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("ProxyChainLabels(%#v) = %v, want %v", tt.proxies, got, tt.want)
			}
			for _, p := range tt.proxies {
				for _, label := range got {
					if p.Password != "" && strings.Contains(label, p.Password) {
						t.Fatalf("label %q leaks the proxy password", label)
					}
					if p.Username != "" && strings.Contains(label, p.Username) {
						t.Fatalf("label %q leaks the proxy username", label)
					}
				}
			}
		})
	}
}

// TestLinkStatusReflectsUpAfterSuccessfulDial covers the direct (no proxy) case: after a successful dial the
// link reports "up", no proxy chain, and a recent LastSuccess.
func TestLinkStatusReflectsUpAfterSuccessfulDial(t *testing.T) {
	ca, err := pki.NewCA("link status test CA")
	if err != nil {
		t.Fatal(err)
	}
	peerAddr := freeAddress(t)
	peer := newOverlay(t, identityFor(t, ca, "peer", 1))
	apply(t, peer, snapshot.Snapshot{NodeID: "peer", RelayListen: peerAddr,
		Relay: []snapshot.RelayRoute{{Key: "k", From: []string{"client"}, Target: "unused"}}})

	l := newLinkWithoutMaintain(identityFor(t, ca, "client", 1), "peer", peerAddr, nil,
		func() (int64, bool) { return 1, true }, zap.NewNop())
	if status := l.status(); status.Status != "down" {
		t.Fatalf("status before any dial = %q, want down", status.Status)
	}

	before := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cc, err := l.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire = %v, want success", err)
	}
	defer cc.Close()

	status := l.status()
	if status.Status != "up" {
		t.Fatalf("status = %q, want up", status.Status)
	}
	if status.Proxies != nil {
		t.Fatalf("direct link reported a proxy chain: %v", status.Proxies)
	}
	if status.LastError != "" {
		t.Fatalf("LastError = %q, want empty after a successful dial", status.LastError)
	}
	if status.LastSuccess == "" {
		t.Fatal("LastSuccess was not set after a successful dial")
	}
	success, err := time.Parse(time.RFC3339, status.LastSuccess)
	if err != nil {
		t.Fatalf("LastSuccess = %q is not RFC3339: %v", status.LastSuccess, err)
	}
	if success.Before(before.Add(-2 * time.Second)) {
		t.Fatalf("LastSuccess = %v, want close to %v", success, before)
	}
}

// TestLinkStatusReportsDialFailureWithoutLeakingProxyCredentials covers a real (not merely canceled) dial
// failure through a proxy chain: the link reports "down", the chain that failed, and an error that names the
// failure without the proxy's password.
func TestLinkStatusReportsDialFailureWithoutLeakingProxyCredentials(t *testing.T) {
	ca, err := pki.NewCA("link status test CA")
	if err != nil {
		t.Fatal(err)
	}
	const password = "super-secret-password"
	socks := startSocks(t, "alice", password)
	proxies := []snapshot.Proxy{{Type: "socks5", Address: socks.addr(), Username: "alice", Password: "wrong-password"}}

	l := newLinkWithoutMaintain(identityFor(t, ca, "client", 1), "peer", "203.0.113.1:1", proxies,
		func() (int64, bool) { return 1, true }, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.acquire(ctx); err == nil {
		t.Fatal("acquire through a proxy that rejects the credentials should fail")
	}

	status := l.status()
	if status.Status != "down" {
		t.Fatalf("status = %q, want down", status.Status)
	}
	if status.Failures != 1 {
		t.Fatalf("failures = %d, want 1", status.Failures)
	}
	if !slices.Equal(status.Proxies, []string{"socks5://" + socks.addr()}) {
		t.Fatalf("proxies = %v", status.Proxies)
	}
	if status.LastError == "" {
		t.Fatal("LastError was not set after a dial failure")
	}
	if status.DownUntil == "" {
		t.Fatal("DownUntil was not set after a dial failure")
	}
	if strings.Contains(status.LastError, password) || strings.Contains(status.LastError, "wrong-password") {
		t.Fatalf("LastError leaked a proxy credential: %q", status.LastError)
	}
}

// TestLinkStatusReportsDialingWhileADialIsInFlight uses a listener that accepts TCP but never completes a TLS
// handshake, so the dial stays in flight long enough to observe the "dialing" state.
func TestLinkStatusReportsDialingWhileADialIsInFlight(t *testing.T) {
	ca, err := pki.NewCA("link status test CA")
	if err != nil {
		t.Fatal(err)
	}
	blackHoleAddr := freeAddress(t)
	blackHole, err := net.Listen("tcp", blackHoleAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer blackHole.Close()
	go func() {
		for {
			conn, err := blackHole.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	l := newLinkWithoutMaintain(identityFor(t, ca, "client", 1), "peer", blackHoleAddr, nil,
		func() (int64, bool) { return 1, true }, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := l.acquire(ctx); done <- err }()

	dialing := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.status().Status == "dialing" {
			dialing = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !dialing {
		t.Fatal(`status never reported "dialing" while a dial was in flight`)
	}
}
