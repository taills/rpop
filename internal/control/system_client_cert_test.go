package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

func TestSystemClientCertificateNormalization(t *testing.T) {
	material := makeTestTLSMaterial(t)
	settings, _, err := normalizeSystemSettings(systemSettings{ClientCertificates: []systemClientCertificate{{
		Name: "  partner mTLS  ", CertificatePEM: string(material.clientPEM), PrivateKeyPEM: string(material.clientKeyPEM),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.ClientCertificates) != 1 {
		t.Fatalf("unexpected client certificates: %#v", settings.ClientCertificates)
	}
	certificate := settings.ClientCertificates[0]
	if certificate.Name != "partner mTLS" || len(certificate.ID) != 64 || !strings.HasPrefix(certificate.CertificatePEM, "-----BEGIN CERTIFICATE-----") || certificate.PrivateKeyPEM == "" {
		t.Fatalf("client certificate was not normalized: %#v", certificate)
	}

	cases := map[string]systemClientCertificate{
		"missing key":        {Name: "x", CertificatePEM: string(material.clientPEM)},
		"mismatched key":     {Name: "x", CertificatePEM: string(material.clientPEM), PrivateKeyPEM: string(material.serverKeyPEM)},
		"server-only usage":  {Name: "x", CertificatePEM: string(material.serverPEM), PrivateKeyPEM: string(material.serverKeyPEM)},
		"leading junk":       {Name: "x", CertificatePEM: "junk\n" + string(material.clientPEM), PrivateKeyPEM: string(material.clientKeyPEM)},
		"CA as client cert":  {Name: "x", CertificatePEM: string(material.rootPEM), PrivateKeyPEM: string(material.clientKeyPEM)},
		"encrypted key text": {Name: "x", CertificatePEM: string(material.clientPEM), PrivateKeyPEM: "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAA==\n-----END ENCRYPTED PRIVATE KEY-----"},
		"legacy encrypted":   {Name: "x", CertificatePEM: string(material.clientPEM), PrivateKeyPEM: "-----BEGIN EC PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,00000000000000000000000000000000\n\nAA==\n-----END EC PRIVATE KEY-----"},
	}
	for name, item := range cases {
		if _, _, err := normalizeSystemSettings(systemSettings{ClientCertificates: []systemClientCertificate{item}}); err == nil {
			t.Errorf("%s: accepted an invalid client certificate", name)
		}
	}
	duplicate := systemClientCertificate{Name: "x", CertificatePEM: string(material.clientPEM), PrivateKeyPEM: string(material.clientKeyPEM)}
	if _, _, err := normalizeSystemSettings(systemSettings{ClientCertificates: []systemClientCertificate{duplicate, duplicate}}); err == nil {
		t.Error("accepted duplicate client certificates")
	}
}

func TestSystemClientCertificateAPIHidesKeysAndProtectsReferences(t *testing.T) {
	material := makeTestTLSMaterial(t)
	s, _ := openSystemCATestStore(t)
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)
	call := func(method, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	put := func(value any) *httptest.ResponseRecorder {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return call(http.MethodPut, string(body))
	}

	response := put(map[string]any{"timeZone": "UTC", "clientCertificates": []map[string]string{{
		"name": "partner", "certificatePem": string(material.clientPEM), "privateKeyPem": string(material.clientKeyPEM),
	}}})
	if response.Code != http.StatusOK {
		t.Fatalf("could not add client certificate: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "PRIVATE KEY") {
		t.Fatalf("PUT response leaked the private key: %s", response.Body.String())
	}
	var saved systemSettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil || len(saved.ClientCertificates) != 1 {
		t.Fatalf("unexpected PUT response: %s", response.Body.String())
	}
	view := saved.ClientCertificates[0]
	if !view.HasPrivateKey || view.Subject == "" || view.NotAfter == "" {
		t.Fatalf("client certificate view is incomplete: %#v", view)
	}
	if response := call(http.MethodGet, ""); response.Code != http.StatusOK || strings.Contains(response.Body.String(), "PRIVATE KEY") {
		t.Fatalf("GET leaked the private key or failed: status=%d body=%s", response.Code, response.Body.String())
	}

	// Renaming without resubmitting the key keeps the stored key.
	response = put(map[string]any{"timeZone": "UTC", "clientCertificates": []map[string]string{{
		"id": view.ID, "name": "partner renamed", "certificatePem": view.CertificatePEM,
	}}})
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &saved) != nil || saved.ClientCertificates[0].Name != "partner renamed" {
		t.Fatalf("could not rename client certificate: status=%d body=%s", response.Code, response.Body.String())
	}
	// Saving other settings without clientCertificates keeps them intact.
	if response := call(http.MethodPut, `{"timeZone":"Asia/Shanghai","rootCertificates":[]}`); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), view.ID) {
		t.Fatalf("omitting clientCertificates dropped them: status=%d body=%s", response.Code, response.Body.String())
	}

	site := store.Site{ID: "client-user", Name: "Client User", Config: store.Config{
		ListenAddress: "127.0.0.1", ListenPort: 18083,
		Upstreams: []store.Upstream{{URL: "https://upstream.test", ClientCertificateID: view.ID}},
	}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if response := put(map[string]any{"timeZone": "UTC", "clientCertificates": []any{}}); response.Code != http.StatusBadRequest {
		t.Fatalf("removed an in-use client certificate: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSiteValidationForSystemClientCertificate(t *testing.T) {
	base := store.Site{ID: "s", Name: "S", Config: store.Config{ListenAddress: "127.0.0.1", ListenPort: 18084}}
	plain := base
	plain.Config.Upstreams = []store.Upstream{{URL: "http://upstream.test", ClientCertificateID: "abc"}}
	if err := validate(plain); err == nil {
		t.Error("accepted a system client certificate on an HTTP upstream")
	}
	both := base
	both.Config.Upstreams = []store.Upstream{{URL: "https://upstream.test", ClientCertificateID: "abc", ClientCertSecret: "c", ClientKeySecret: "k"}}
	if err := validate(both); err == nil {
		t.Error("accepted both a system client certificate and site client secrets")
	}

	s, _ := openSystemCATestStore(t)
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	missing := base
	missing.Config.Upstreams = []store.Upstream{{URL: "https://upstream.test", ClientCertificateID: "does-not-exist"}}
	if err := control.validateSystemCertificateReferences(missing); err == nil {
		t.Error("accepted a reference to a missing system client certificate")
	}
}

func TestProxyHandlerUsesSystemClientCertificate(t *testing.T) {
	material := makeTestTLSMaterial(t)
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(material.rootPEM) {
		t.Fatal("could not add test CA to the backend client-auth pool")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "rpop-client" {
			http.Error(w, "client certificate was not presented", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("system mTLS verified"))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{material.serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()

	s, _ := openSystemCATestStore(t)
	settings, _, err := normalizeSystemSettings(systemSettings{
		RootCertificates:   []systemRootCertificate{{Name: "RPOP test CA", PEM: string(material.rootPEM)}},
		ClientCertificates: []systemClientCertificate{{Name: "RPOP client", CertificatePEM: string(material.clientPEM), PrivateKeyPEM: string(material.clientKeyPEM)}},
	})
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
	site := store.Site{ID: "system-mtls", Name: "System mTLS", Config: store.Config{
		ListenAddress: "127.0.0.1", ListenPort: 18085,
		Upstreams: []store.Upstream{{
			URL: "https://upstream.test/verify", DialAddress: server.Listener.Addr().String(), ServerName: "upstream.test",
			RootCertificateIDs: []string{settings.RootCertificates[0].ID}, ClientCertificateID: settings.ClientCertificates[0].ID,
		}},
	}}
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
	if response.Code != http.StatusOK || response.Body.String() != "system mTLS verified" {
		t.Fatalf("system client certificate was not used: status=%d body=%s", response.Code, response.Body.String())
	}
}
