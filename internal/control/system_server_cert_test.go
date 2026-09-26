package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

type testServerCertificate struct {
	certificatePEM string
	keyPEM         string
}

func makeSelfSignedLeaf(t *testing.T, commonName string, usage x509.ExtKeyUsage, dnsNames ...string) testServerCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		DNSNames: dnsNames, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return testServerCertificate{
		certificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		keyPEM:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}
}

func TestSystemServerCertificateNormalization(t *testing.T) {
	wildcard := makeSelfSignedLeaf(t, "*.example.test", x509.ExtKeyUsageServerAuth, "*.example.test", "example.test")
	settings, _, err := normalizeSystemSettings(systemSettings{ServerCertificates: []systemServerCertificate{{
		CertificatePEM: wildcard.certificatePEM, PrivateKeyPEM: wildcard.keyPEM,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	first := settings.ServerCertificates[0]
	if first.Name != "*.example.test" || !stableCertificateIDPattern.MatchString(first.ID) {
		t.Fatalf("server certificate was not normalized with a generated stable ID: %#v", first)
	}
	again, _, err := normalizeSystemSettings(settings)
	if err != nil || again.ServerCertificates[0].ID != first.ID {
		t.Fatalf("stable server certificate ID changed on re-normalization: %#v, %v", again.ServerCertificates, err)
	}

	clientOnly := makeSelfSignedLeaf(t, "client", x509.ExtKeyUsageClientAuth)
	cases := map[string]systemServerCertificate{
		"client-only usage": {CertificatePEM: clientOnly.certificatePEM, PrivateKeyPEM: clientOnly.keyPEM},
		"missing key":       {CertificatePEM: wildcard.certificatePEM},
		"invalid id":        {ID: "../../etc", CertificatePEM: wildcard.certificatePEM, PrivateKeyPEM: wildcard.keyPEM},
	}
	for name, item := range cases {
		if _, _, err := normalizeSystemSettings(systemSettings{ServerCertificates: []systemServerCertificate{item}}); err == nil {
			t.Errorf("%s: accepted an invalid server certificate", name)
		}
	}
	other := makeSelfSignedLeaf(t, "other", x509.ExtKeyUsageServerAuth, "other.test")
	sameID := []systemServerCertificate{
		{ID: first.ID, CertificatePEM: wildcard.certificatePEM, PrivateKeyPEM: wildcard.keyPEM},
		{ID: first.ID, CertificatePEM: other.certificatePEM, PrivateKeyPEM: other.keyPEM},
	}
	if _, _, err := normalizeSystemSettings(systemSettings{ServerCertificates: sameID}); err == nil {
		t.Error("accepted duplicate server certificate IDs")
	}
}

func TestSiteValidationForSystemServerCertificate(t *testing.T) {
	site := store.Site{ID: "s", Name: "S", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: 18090, CertificateID: "0123456789abcdef0123456789abcdef",
		Upstreams: []store.Upstream{{URL: "http://upstream.test"}}}}
	if err := validate(site); err == nil {
		t.Error("accepted certificateId without TLS")
	}
	site.Config.TLS = true
	site.Config.CertificateSecret, site.Config.PrivateKeySecret = "c", "k"
	if err := validate(site); err == nil {
		t.Error("accepted both a system server certificate and site certificate secrets")
	}
	site.Config.CertificateSecret, site.Config.PrivateKeySecret = "", ""
	if err := validate(site); err != nil {
		t.Fatalf("rejected a valid system server certificate reference: %v", err)
	}
	s, _ := openSystemCATestStore(t)
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	if err := control.validateSystemCertificateReferences(site); err == nil {
		t.Error("accepted a reference to a missing system server certificate")
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func presentedCommonName(t *testing.T, port int, serverName string) string {
	t.Helper()
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS handshake for %s failed: %v", serverName, err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
}

func TestSharedWildcardServerCertificateRenewsRunningSites(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	defer control.StopAll()
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)
	put := func(value any) (*httptest.ResponseRecorder, systemSettingsResponse) {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(body)))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var decoded systemSettingsResponse
		_ = json.Unmarshal(response.Body.Bytes(), &decoded)
		return response, decoded
	}

	original := makeSelfSignedLeaf(t, "wildcard-v1", x509.ExtKeyUsageServerAuth, "*.example.test")
	response, saved := put(map[string]any{"timeZone": "UTC", "serverCertificates": []map[string]string{{
		"name": "example wildcard", "certificatePem": original.certificatePEM, "privateKeyPem": original.keyPEM,
	}}})
	if response.Code != http.StatusOK || len(saved.ServerCertificates) != 1 || strings.Contains(response.Body.String(), "PRIVATE KEY") {
		t.Fatalf("could not add server certificate: status=%d body=%s", response.Code, response.Body.String())
	}
	view := saved.ServerCertificates[0]
	if len(view.DNSNames) != 1 || view.DNSNames[0] != "*.example.test" {
		t.Fatalf("server certificate view is missing DNS names: %#v", view)
	}

	port := freeLoopbackPort(t)
	for _, host := range []string{"a.example.test", "b.example.test"} {
		site := store.Site{ID: strings.Split(host, ".")[0], Name: host, Config: store.Config{
			ListenAddress: "127.0.0.1", ListenPort: port, TLS: true, CertificateID: view.ID, Hostnames: []string{host},
			Upstreams: []store.Upstream{{URL: "http://127.0.0.1:9"}},
		}}
		if err := control.validateSystemCertificateReferences(site); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(context.Background(), site); err != nil {
			t.Fatal(err)
		}
		if err := control.start(context.Background(), site.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"a.example.test", "b.example.test"} {
		if name := presentedCommonName(t, port, host); name != "wildcard-v1" {
			t.Fatalf("%s presented %q, want the shared wildcard certificate", host, name)
		}
	}

	renewed := makeSelfSignedLeaf(t, "wildcard-v2", x509.ExtKeyUsageServerAuth, "*.example.test")
	response, saved = put(map[string]any{"timeZone": "UTC", "serverCertificates": []map[string]string{{
		"id": view.ID, "name": "example wildcard", "certificatePem": renewed.certificatePEM, "privateKeyPem": renewed.keyPEM,
	}}})
	if response.Code != http.StatusOK || saved.ServerCertificates[0].ID != view.ID {
		t.Fatalf("could not renew the shared certificate in place: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, host := range []string{"a.example.test", "b.example.test"} {
		if name := presentedCommonName(t, port, host); name != "wildcard-v2" {
			t.Fatalf("%s presented %q after renewal, want the renewed certificate without restart", host, name)
		}
	}

	if response, _ := put(map[string]any{"timeZone": "UTC", "serverCertificates": []any{}}); response.Code != http.StatusBadRequest {
		t.Fatalf("removed an in-use server certificate: status=%d body=%s", response.Code, response.Body.String())
	}
}
