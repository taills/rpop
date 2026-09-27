package overlay

import (
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
)

// TestRelayDrainFreesTheAddressImmediately covers a LOW review finding: drain used to close the listener only
// from inside a background goroutine's call to server.Shutdown, and nothing guaranteed that goroutine ran
// before the caller went on to start a new relay port at the same address. A node that lost and regained its
// relay route in quick succession (two Apply calls back to back) could then hit "address already in use".
// drain must free the address before it returns, even though tunnels already open keep draining afterward.
func TestRelayDrainFreesTheAddressImmediately(t *testing.T) {
	ca, err := pki.NewCA("relay drain test CA")
	if err != nil {
		t.Fatal(err)
	}
	o := New(identityFor(t, ca, "node1", 1), zap.NewNop())
	address := freeAddress(t)

	for i := range 20 {
		relay, err := o.startRelay(address)
		if err != nil {
			t.Fatalf("startRelay attempt %d at %s: %v", i, address, err)
		}
		relay.drain()
	}
}
