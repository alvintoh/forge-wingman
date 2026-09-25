package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMux(t *testing.T) {
	spa := fstest.MapFS{
		"index.html":    {Data: []byte("<html>shell</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	mux := newMux(spa)

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantBody string
	}{
		{"health check", "/healthz", http.StatusOK, ""},
		{"built asset", "/assets/app.js", http.StatusOK, "console.log(1)"},
		{"client route falls back to the shell", "/runs/abc", http.StatusOK, "shell"},
		{"unknown api endpoint is not the shell", "/api/nope", http.StatusNotFound, ""},
		{"missing asset is not the shell", "/favicon.ico", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	mux := newMux(fstest.MapFS{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"sha\":\"dev\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
