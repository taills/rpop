package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/store"
)

// TestSiteAPIGetSingleSite covers GET /api/sites/{id}, which the console needs for a per-site detail view (it
// was previously missing: the site() handler only handled PUT/DELETE at the /api/sites/{id} path, so GET fell
// through to the generic 405 at the bottom of the handler). The single-site view must match the corresponding
// entry in GET /api/sites (Running included) and must not leak a referenced secret's raw content, same as the
// list endpoint (store.Site only ever carries secret *references*, never their bytes, but this pins that down
// as a regression test rather than relying on it being true by construction).
func TestSiteAPIGetSingleSite(t *testing.T) {
	c := newTestControl(t)
	handler := c.Handler()
	cookie := setupAdminForTest(t, handler)

	site := store.Site{ID: "web", Name: "Web", Config: store.Config{
		ListenAddress: "127.0.0.1", ListenPort: 8443,
		Upstreams: []store.Upstream{{URL: "https://upstream.test", ClientCertSecret: "client-cert", ClientKeySecret: "client-key"}},
	}}
	if err := c.store.Save(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SaveSecret(context.Background(), site.ID, "client-key", []byte("very secret private key bytes")); err != nil {
		t.Fatal(err)
	}

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	cases := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "existing site", path: "/api/sites/web", wantStatus: http.StatusOK},
		{name: "unknown site", path: "/api/sites/missing", wantStatus: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := get(tc.path)
			if response.Code != tc.wantStatus {
				t.Fatalf("GET %s = %d, want %d: %s", tc.path, response.Code, tc.wantStatus, response.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if strings.Contains(response.Body.String(), "very secret private key bytes") {
				t.Fatal("single site view leaked a secret's raw content")
			}
			var got store.Site
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}

			var list []store.Site
			if err := json.Unmarshal(get("/api/sites").Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			found := false
			var want store.Site
			for _, s := range list {
				if s.ID == got.ID {
					want, found = s, true
				}
			}
			if !found {
				t.Fatalf("site %q not present in list view", got.ID)
			}
			if got.Name != want.Name || got.Running != want.Running || got.Config.ListenPort != want.Config.ListenPort {
				t.Fatalf("single site view = %#v, want %#v", got, want)
			}
			if got.Config.Upstreams[0].ClientCertSecret != "client-cert" {
				t.Fatalf("client cert secret reference lost: %#v", got.Config.Upstreams[0])
			}
		})
	}
}

func TestSiteAPIGetSingleSiteRequiresAuthentication(t *testing.T) {
	handler := newTestControl(t).Handler()
	setupAdminForTest(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/api/sites/web", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/sites/{id} = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
