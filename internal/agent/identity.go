package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/southbound"
)

const (
	keyFile  = "node.key"
	certFile = "node.crt"
	caFile   = "ca.crt"

	renewBefore        = 30 * 24 * time.Hour
	renewCheckInterval = time.Hour
	renewRetryInterval = 5 * time.Minute
	maxResponseBytes   = 1 << 20
)

// statusError is a non-2xx answer from the controller.
type statusError struct {
	code    int
	message string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("controller answered %d: %s", e.code, e.message)
}

func isUnauthorized(err error) bool {
	var status *statusError
	return errors.As(err, &status) && (status.code == http.StatusUnauthorized || status.code == http.StatusForbidden)
}

// isUpgradeRequired reports whether the controller rejected a request over the southbound protocol version
// window (D27): retrying it immediately cannot help, since nothing changes until the node or the controller is
// upgraded, so callers back off longer than they would for a transient failure.
func isUpgradeRequired(err error) bool {
	var status *statusError
	return errors.As(err, &status) && status.code == http.StatusUpgradeRequired
}

func responseError(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	var decoded struct {
		Error string `json:"error"`
	}
	message := string(bytes.TrimSpace(body))
	if json.Unmarshal(body, &decoded) == nil && decoded.Error != "" {
		message = decoded.Error
	}
	return &statusError{code: response.StatusCode, message: message}
}

func newClient(config *tls.Config) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       config,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		// Pings detect a dead connection under a silent watch stream sooner than TCP would.
		HTTP2: &http.HTTP2Config{SendPingTimeout: southbound.PingInterval, PingTimeout: 10 * time.Second},
	}}
}

// postJSON sends in and decodes the answer into out, if out is not nil.
func postJSON(ctx context.Context, client *http.Client, endpoint string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(southbound.ProtocolVersionHeader, strconv.Itoa(southbound.ProtocolVersion))
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return err
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(out)
}

// loadIdentity reads the node's key and certificates; it wraps os.ErrNotExist when the node never registered.
func loadIdentity(dir string) (*pki.Identity, error) {
	var contents [3]string
	for i, name := range []string{certFile, keyFile, caFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read node identity: %w", err)
		}
		contents[i] = string(data)
	}
	identity, err := pki.LoadIdentity(contents[0], contents[1], contents[2])
	if err != nil {
		return nil, fmt.Errorf("load node identity from %s: %w", dir, err)
	}
	return identity, nil
}

// register exchanges a join token for a certificate. The controller is authenticated by the CA fingerprint the
// token pins, and the new private key never leaves the node.
func register(ctx context.Context, base *url.URL, dir, joinToken string) (*pki.Identity, error) {
	token, err := pki.ParseJoinToken(joinToken)
	if err != nil {
		return nil, err
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(token.NodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	client := newClient(pki.BootstrapClientConfig(token.CAFingerprint))
	defer client.CloseIdleConnections()
	var response southbound.RegisterResponse
	request := southbound.RegisterRequest{Token: joinToken, CSRPEM: csrPEM}
	if err := postJSON(ctx, client, base.JoinPath(southbound.RegisterPath).String(), request, &response); err != nil {
		return nil, err
	}
	ca, err := pki.ParseCertificate(response.CAPEM)
	if err != nil {
		return nil, fmt.Errorf("controller returned an invalid CA: %w", err)
	}
	if pki.Fingerprint(ca.Raw) != token.CAFingerprint {
		return nil, errors.New("controller returned a CA other than the one the join token pins")
	}
	identity, err := pki.LoadIdentity(response.CertificatePEM, keyPEM, response.CAPEM)
	if err != nil {
		return nil, err
	}
	if identity.NodeID != token.NodeID {
		return nil, fmt.Errorf("controller issued a certificate for node %q, want %q", identity.NodeID, token.NodeID)
	}
	for _, file := range []struct{ name, data string }{{keyFile, keyPEM}, {certFile, response.CertificatePEM}, {caFile, response.CAPEM}} {
		if err := writeFileAtomic(filepath.Join(dir, file.name), []byte(file.data)); err != nil {
			return nil, fmt.Errorf("store node identity: %w", err)
		}
	}
	return identity, nil
}

// renewLoop renews the node certificate well before it expires.
func (a *Agent) renewLoop(ctx context.Context) {
	for {
		wait := renewCheckInterval
		leaf := a.current.Load().identity.Certificate().Leaf
		if time.Until(leaf.NotAfter) < renewBefore {
			if err := a.renew(ctx); err != nil {
				a.log.Warn("certificate renewal failed", zap.Error(err), zap.Time("expires", leaf.NotAfter))
				wait = renewRetryInterval
			}
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

func (a *Agent) renew(ctx context.Context) error {
	s := a.current.Load()
	key, err := s.identity.PrivateKey()
	if err != nil {
		return err
	}
	csrPEM, err := pki.CSRForKey(key, s.identity.NodeID)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(filepath.Join(a.cfg.DataDir, keyFile))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var response southbound.RenewResponse
	if err := postJSON(ctx, s.client, a.endpoint(southbound.RenewPath), southbound.RenewRequest{CSRPEM: csrPEM}, &response); err != nil {
		return err
	}
	if err := s.identity.SetCertificate(response.CertificatePEM, string(keyPEM)); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(a.cfg.DataDir, certFile), []byte(response.CertificatePEM)); err != nil {
		return fmt.Errorf("store renewed certificate: %w", err)
	}
	a.log.Info("node certificate renewed", zap.Time("expires", s.identity.Certificate().Leaf.NotAfter))
	return nil
}

// writeFileAtomic replaces a file readable only by its owner, so a crash never leaves it half written.
func writeFileAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
