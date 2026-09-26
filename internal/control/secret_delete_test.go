package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

func TestDeleteSiteSecretAPI(t *testing.T) {
	s, _ := openSystemCATestStore(t)
	control, err := NewWithLogDir(s, zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseAccessLogs(context.Background())
	handler := control.Handler()
	cookie := setupAdminForTest(t, handler)
	site := store.Site{ID: "secret-site", Name: "Secret Site", Config: store.Config{
		Upstreams: []store.Upstream{{URL: "https://upstream.test", ClientCertSecret: "client-cert", ClientKeySecret: "client-key"}},
	}}
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(context.Background(), site.ID, "client-key", []byte("private material")); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/sites/secret-site/secrets/client-key", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("referenced secret delete status=%d body=%s", response.Code, response.Body.String())
	}
	site.Config.Upstreams[0].ClientCertSecret = ""
	site.Config.Upstreams[0].ClientKeySecret = ""
	if err := s.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodDelete, "/api/sites/secret-site/secrets/client-key", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("secret delete status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := s.Secret(context.Background(), site.ID, "client-key"); err != store.ErrNotFound {
		t.Fatalf("deleted client key remains available: err=%v", err)
	}
}
