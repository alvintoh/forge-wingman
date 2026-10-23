package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestEgressProxyReadyReportsWhetherTheProxyAcceptsConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := egressProxy(context.Background(), slog.New(slog.DiscardHandler), []string{"-ready", addr}); err != nil {
		t.Fatalf("-ready against a listener: %v", err)
	}
	_ = ln.Close()
	if err := egressProxy(context.Background(), slog.New(slog.DiscardHandler), []string{"-ready", addr}); err == nil {
		t.Fatal("-ready succeeded with nothing listening")
	}
}

func TestEgressProxyLogsItsSummaryWhenItStops(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- egressProxy(ctx, slog.New(slog.NewJSONHandler(&lockedWriter{w: &buf}, nil)), []string{"-allow", "api.example.com", "-listen", "127.0.0.1:0"})
	}()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := (&lockedWriter{w: &buf}).String(); !strings.Contains(got, `"msg":"egressSummary"`) {
		t.Fatalf("log %q, want a summary on an orderly stop", got)
	}
}

// lockedWriter is a log sink safe to read after the writer has stopped.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string { return l.w.String() }

func TestEgressServerDropsSilentConnections(t *testing.T) {
	srv := egressServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("server timeouts header %v idle %v, want both set", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
}

func TestEgressProxyNeedsAHostToAllow(t *testing.T) {
	if err := egressProxy(context.Background(), slog.New(slog.DiscardHandler), nil); err == nil {
		t.Fatal("egressProxy served with nothing allowed")
	}
}
