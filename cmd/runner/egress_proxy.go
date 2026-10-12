package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const egressShutdownTimeout = 5 * time.Second

// egressProxy serves the CONNECT proxy that confines the agent container to its
// models' API hosts until ctx ends. With -ready it instead exits 0 when the
// proxy at that address accepts a connection.
func egressProxy(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("egress-proxy", flag.ContinueOnError)
	listen := fs.String("listen", ":3128", "address to serve on")
	allow := fs.String("allow", "", "comma-separated hostnames to tunnel to on port 443")
	ready := fs.String("ready", "", "address to dial once, exiting 0 when it accepts a connection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ready != "" {
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", *ready)
		if err != nil {
			return fmt.Errorf("egress proxy readiness: %w", err)
		}
		return c.Close()
	}
	var hosts []string
	for _, h := range strings.Split(*allow, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return errors.New("usage: runner egress-proxy -allow <host,...> [-listen <addr>]")
	}
	srv := &http.Server{Addr: *listen, Handler: runner.NewEgressProxy(hosts, logger), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("egressProxyListening", "addr", *listen, "hosts", len(hosts))
	select {
	case err := <-errc:
		return fmt.Errorf("egress proxy: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), egressShutdownTimeout)
	defer cancel()
	return srv.Shutdown(sctx)
}
