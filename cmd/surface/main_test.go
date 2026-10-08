package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
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

func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		info       *debug.BuildInfo
		ok         bool
		wantCommit string
	}{
		{
			name:       "revision from a git checkout",
			info:       &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "1a2b3c4"}}},
			ok:         true,
			wantCommit: "1a2b3c4",
		},
		{
			name:       "build info without a revision",
			info:       &debug.BuildInfo{},
			ok:         true,
			wantCommit: "unknown",
		},
		{
			name:       "binary without build info",
			info:       nil,
			ok:         false,
			wantCommit: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readBuildInfo = func() (*debug.BuildInfo, bool) { return tt.info, tt.ok }
			t.Cleanup(func() { readBuildInfo = debug.ReadBuildInfo })

			rec := httptest.NewRecorder()
			newMux(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			var got struct {
				Status string `json:"status"`
				Commit string `json:"commit"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
			}
			if got.Status != "ok" {
				t.Errorf("status field = %q, want %q", got.Status, "ok")
			}
			if got.Commit != tt.wantCommit {
				t.Errorf("commit field = %q, want %q", got.Commit, tt.wantCommit)
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
