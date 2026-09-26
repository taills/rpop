package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

type testTLSMaterial struct {
	rootPEM      []byte
	serverPEM    []byte
	serverKeyPEM []byte
	clientPEM    []byte
	clientKeyPEM []byte
	serverCert   tls.Certificate
}

func makeTestTLSMaterial(t *testing.T) testTLSMaterial {
	t.Helper()
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "RPOP Test Root CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})

	makeLeaf := func(serial int64, commonName string, usage x509.ExtKeyUsage, dnsNames []string) ([]byte, []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber:          big.NewInt(serial),
			Subject:               pkix.Name{CommonName: commonName},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.Add(24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{usage},
			DNSNames:              dnsNames,
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, rootTemplate, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}

	serverPEM, serverKeyPEM := makeLeaf(2, "upstream.test", x509.ExtKeyUsageServerAuth, []string{"upstream.test"})
	serverCert, err := tls.X509KeyPair(serverPEM, serverKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	clientPEM, clientKeyPEM := makeLeaf(3, "rpop-client", x509.ExtKeyUsageClientAuth, nil)
	return testTLSMaterial{
		rootPEM: rootPEM, serverPEM: serverPEM, serverKeyPEM: serverKeyPEM,
		clientPEM: clientPEM, clientKeyPEM: clientKeyPEM, serverCert: serverCert,
	}
}

func openSystemCATestStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.New(db), db
}

func TestSystemRootCertificateValidationAndReferenceProtection(t *testing.T) {
	material := makeTestTLSMaterial(t)
	settings, _, err := normalizeSystemSettings(systemSettings{RootCertificates: []systemRootCertificate{{Name: "  corporate root  ", PEM: string(material.rootPEM)}}})
	if err != nil {
		t.Fatal(err)
	}
	if settings.TimeZone != "UTC" || len(settings.RootCertificates) != 1 {
		t.Fatalf("unexpected normalized settings: %#v", settings)
	}
	root := settings.RootCertificates[0]
	if root.Name != "corporate root" || len(root.ID) != 64 || !strings.HasPrefix(root.PEM, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("root certificate was not normalized: %#v", root)
	}
	if _, _, err := normalizeSystemSettings(systemSettings{RootCertificates: []systemRootCertificate{{Name: "leaf", PEM: string(material.serverPEM)}}}); err == nil {
		t.Fatal("accepted a non-CA certificate as a system root")
	}
	if _, _, err := normalizeSystemSettings(systemSettings{RootCertificates: []systemRootCertificate{{PEM: "junk\n-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----"}}}); err == nil {
		t.Fatal("accepted data preceding the PEM certificate block")
	}

	s, _ := openSystemCATestStore(t)
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)
	put := func(value systemSettings) *httptest.ResponseRecorder {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(body)))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := put(settings)
	var saved systemSettings
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &saved) != nil || len(saved.RootCertificates) != 1 {
		t.Fatalf("could not persist system root certificate: status=%d body=%s", response.Code, response.Body.String())
	}
	site := store.Site{ID: "root-user", Name: "Root User", Config: store.Config{
		ListenAddress: "127.0.0.1", ListenPort: 18081,
		Upstreams: []store.Upstream{{URL: "https://upstream.test", RootCertificateIDs: []string{saved.RootCertificates[0].ID}}},
	}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if response := put(systemSettings{TimeZone: "UTC"}); response.Code != http.StatusBadRequest {
		t.Fatalf("removed an in-use system root certificate: status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := s.GetSetting(context.Background(), systemSettingsSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	var persisted systemSettings
	if err := json.Unmarshal(stored, &persisted); err != nil || len(persisted.RootCertificates) != 1 {
		t.Fatalf("failed removal changed persisted settings: %#v, %v", persisted, err)
	}
}

func TestProxyHandlerUsesSelectedSystemCAAndMTLSClientCertificate(t *testing.T) {
	material := makeTestTLSMaterial(t)
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(material.rootPEM) {
		t.Fatal("could not add test CA to the backend client-auth pool")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 || r.TLS.PeerCertificates[0].Subject.CommonName != "rpop-client" {
			http.Error(w, "client certificate was not presented", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mTLS verified"))
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{material.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientRoots,
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	defer server.Close()

	s, _ := openSystemCATestStore(t)
	settings, _, err := normalizeSystemSettings(systemSettings{RootCertificates: []systemRootCertificate{{Name: "RPOP test CA", PEM: string(material.rootPEM)}}})
	if err != nil {
		t.Fatal(err)
	}
	settingJSON, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(context.Background(), systemSettingsSettingKey, settingJSON); err != nil {
		t.Fatal(err)
	}
	rootID := settings.RootCertificates[0].ID
	upstream := store.Upstream{
		URL:                "https://upstream.test/verify",
		DialAddress:        server.Listener.Addr().String(),
		ServerName:         "upstream.test",
		RootCertificateIDs: []string{rootID},
		ClientCertSecret:   "client-cert",
		ClientKeySecret:    "client-key",
	}
	site := store.Site{ID: "mtls-site", Name: "mTLS Site", Config: store.Config{
		ListenAddress: "127.0.0.1", ListenPort: 18082, Upstreams: []store.Upstream{upstream},
	}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), site.ID, upstream.ClientCertSecret, material.clientPEM); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), site.ID, upstream.ClientKeySecret, material.clientKeyPEM); err != nil {
		t.Fatal(err)
	}
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	handler, err := control.proxyHandler(context.Background(), site.ID, site.Config)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://proxy.test/verify", nil))
	if response.Code != http.StatusOK || response.Body.String() != "mTLS verified" {
		t.Fatalf("upstream custom CA/mTLS request failed: status=%d body=%s", response.Code, response.Body.String())
	}
}
