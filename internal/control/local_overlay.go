package control

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/snapshot"
)

const (
	// localGeneration is the registration generation of the embedded node's certificate. The controller issues
	// that certificate to itself and never hands it out, so it is never revoked.
	localGeneration = 1
	// localRenewBefore is how long before expiry the embedded node's certificate is replaced.
	localRenewBefore = 30 * 24 * time.Hour
)

// localOverlay is the embedded node's overlay and the identity it authenticates to other nodes with.
type localOverlay struct {
	*overlay.Overlay
	identity *pki.Identity
	keyPEM   string
}

// applyLocalOverlayLocked hands the embedded node's snapshot to its overlay, creating the overlay the first time
// one of its sites has a path through other nodes.
func (c *Control) applyLocalOverlayLocked(ctx context.Context, s snapshot.Snapshot) error {
	if c.overlay == nil {
		if !crossesNodes(s) {
			return nil
		}
		local, err := c.newLocalOverlay(ctx)
		if err != nil {
			return err
		}
		c.overlay = local
		c.engine.SetPathDialer(local)
	} else if err := c.renewLocalIdentity(ctx); err != nil {
		c.log.Warn("could not renew the embedded node's certificate", zap.Error(err))
	}
	return c.overlay.Apply(s)
}

func crossesNodes(s snapshot.Snapshot) bool {
	for _, site := range s.Sites {
		for _, upstream := range site.Upstreams {
			for _, path := range upstream.Paths {
				if path.FirstNode != "" {
					return true
				}
			}
		}
	}
	return false
}

// newLocalOverlay issues the embedded node a certificate from the internal CA and builds its overlay.
func (c *Control) newLocalOverlay(ctx context.Context) (*localOverlay, error) {
	ca, err := c.ensureCA(ctx)
	if err != nil {
		return nil, err
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(LocalNodeID)
	if err != nil {
		return nil, err
	}
	_, certPEM, err := ca.SignNode(csrPEM, LocalNodeID, localGeneration)
	if err != nil {
		return nil, fmt.Errorf("issue the embedded node's certificate: %w", err)
	}
	identity, err := pki.LoadIdentity(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		return nil, err
	}
	local := &localOverlay{Overlay: overlay.New(identity, c.log.Named("overlay"), c.overlayConfig), identity: identity, keyPEM: keyPEM}
	// The embedded node runs in this same process, so its tunnel events go straight to the controller's own
	// store instead of through a spool and an upload (item 4 of stage 5 step 3); its access logs already write
	// directly through registryWriter for the same reason.
	if c.tunnelEvents != nil {
		local.SetTunnelEventSink(localTunnelEventWriter{store: c.tunnelEvents, log: c.log})
	}
	return local, nil
}

// renewLocalIdentity replaces the embedded node's certificate before it expires, since a controller can run
// longer than a node certificate is valid. Links pick the new certificate up on their next handshake.
func (c *Control) renewLocalIdentity(ctx context.Context) error {
	if time.Until(c.overlay.identity.Certificate().Leaf.NotAfter) > localRenewBefore {
		return nil
	}
	ca, err := c.ensureCA(ctx)
	if err != nil {
		return err
	}
	key, err := c.overlay.identity.PrivateKey()
	if err != nil {
		return err
	}
	csrPEM, err := pki.CSRForKey(key, LocalNodeID)
	if err != nil {
		return err
	}
	_, certPEM, err := ca.SignNode(csrPEM, LocalNodeID, localGeneration)
	if err != nil {
		return err
	}
	return c.overlay.identity.SetCertificate(certPEM, c.overlay.keyPEM)
}
