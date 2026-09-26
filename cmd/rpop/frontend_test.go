package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFrontendHandler(t *testing.T) {
	built := fstest.MapFS{
		"index.html":       {Data: []byte("<html>console</html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"assets/style.css": {Data: []byte("body{}")},
	}
	tests := []struct {
		name       string
		fsys       fstest.MapFS
		path       string
		wantStatus int
		wantBody   string
	}{
		{"root serves index", built, "/", http.StatusOK, "console"},
		{"asset served as-is", built, "/assets/app-1.js", http.StatusOK, "console.log(1)"},
		{"client route falls back to index", built, "/sites/demo", http.StatusOK, "console"},
		{"missing asset falls back to index", built, "/assets/missing.js", http.StatusOK, "console"},
		{"traversal falls back to index", built, "/../../etc/passwd", http.StatusOK, "console"},
		{"unbuilt frontend reports clearly", fstest.MapFS{".gitkeep": {}}, "/", http.StatusServiceUnavailable, "frontend is not built"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			frontendHandler(tt.fsys).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
			body, _ := io.ReadAll(recorder.Result().Body)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", recorder.Code, tt.wantStatus, body)
			}
			if !strings.Contains(string(body), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", body, tt.wantBody)
			}
		})
	}
}
