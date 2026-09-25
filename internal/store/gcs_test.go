package store

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("disk read failed")
	}
	r.sent = true
	return copy(p, "partial completions"), nil
}

func TestCreateObject(t *testing.T) {
	var uploads atomic.Int32
	var query atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/upload/") {
			uploads.Add(1)
			query.Store(r.URL.RawQuery)
			_, _ = io.Copy(io.Discard, r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"bucket":"b","name":"completions/1-1.jsonl"}`)
	}))
	defer srv.Close()

	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	b := NewBucket(client, "b")

	t.Run("a failed source commits nothing", func(t *testing.T) {
		uploads.Store(0)
		if err := b.CreateObject(context.Background(), "completions/1-1.jsonl", &failingReader{}); err == nil {
			t.Fatal("CreateObject succeeded on a failed source")
		}
		if n := uploads.Load(); n != 0 {
			t.Fatalf("%d upload requests reached the bucket", n)
		}
	})

	t.Run("a whole source is created only if absent", func(t *testing.T) {
		uploads.Store(0)
		if err := b.CreateObject(context.Background(), "completions/1-1.jsonl", strings.NewReader("{}\n")); err != nil {
			t.Fatal(err)
		}
		if n := uploads.Load(); n != 1 {
			t.Fatalf("%d upload requests, want 1", n)
		}
		if q, _ := query.Load().(string); !strings.Contains(q, "ifGenerationMatch=0") {
			t.Fatalf("upload query %q lacks the does-not-exist precondition", q)
		}
	})
}
