package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestMuxServesAFontFileAsAFont(t *testing.T) {
	spa := fstest.MapFS{
		"index.html":                  {Data: []byte("<html>shell</html>")},
		"fonts/IBMPlexSans-400.woff2": {Data: []byte("wOF2\x00\x01\x00\x00fontbytes")},
	}
	rec := httptest.NewRecorder()
	newMux(Deps{SPA: spa, Logger: discardLogger()}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/IBMPlexSans-400.woff2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
		t.Fatalf("content type = %q, want font/woff2", got)
	}
}
