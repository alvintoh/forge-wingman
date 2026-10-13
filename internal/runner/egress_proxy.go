package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	egressPort        = "443"
	egressDialTimeout = 10 * time.Second
	// egressIdleTimeout closes a tunnel that has moved no bytes for this long; a
	// model call that streams nothing for longer is treated as hung.
	egressIdleTimeout = 5 * time.Minute
	// egressFirstByteTimeout closes a tunnel that has moved no byte at all, so
	// silent tunnels cannot hold every slot.
	egressFirstByteTimeout = 30 * time.Second
	egressMaxTunnels       = 64
	// egressLoggedHostBytes bounds the host a log line carries; a hostname is at most 253.
	egressLoggedHostBytes = 253

	// The proxy's log events; StartEgress's teardown relays lines naming them.
	egressRefusedEvent = "egressRefused"
	egressFailedEvent  = "egressFailed"
)

// EgressProxy is an HTTP CONNECT proxy that tunnels to port 443 of an exact
// set of hostnames and refuses any name with an answer that is not a public
// unicast address. It matches the CONNECT target, not SNI.
type EgressProxy struct {
	allow   map[string]struct{}
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	dial    func(ctx context.Context, addr string) (net.Conn, error)
	logger  *slog.Logger
	idle    time.Duration
	// firstByte is the idle limit until a tunnel's first byte moves.
	firstByte time.Duration
	slots     chan struct{}
}

// NewEgressProxy returns a proxy allowing hosts, resolving through the system
// resolver and dialling directly, logging each refusal and failure to logger.
func NewEgressProxy(hosts []string, logger *slog.Logger) *EgressProxy {
	allow := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		allow[normalizeHost(h)] = struct{}{}
	}
	d := net.Dialer{Timeout: egressDialTimeout}
	return &EgressProxy{
		allow: allow,
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial:      func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) },
		logger:    logger,
		idle:      egressIdleTimeout,
		firstByte: egressFirstByteTimeout,
		slots:     make(chan struct{}, egressMaxTunnels),
	}
}

func normalizeHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }

// reject logs a refusal or failure and answers it; extra is more attrs for the log line.
func (p *EgressProxy) reject(w http.ResponseWriter, status int, event, reason, host, msg string, extra ...any) {
	p.logger.Warn(event, append([]any{"reason", reason, "host", truncate(host, egressLoggedHostBytes)}, extra...)...)
	http.Error(w, msg, status)
}

// ServeHTTP tunnels an allowed CONNECT and answers every other request with an error status.
func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		p.reject(w, http.StatusMethodNotAllowed, egressRefusedEvent, "method-not-connect", r.Host, "CONNECT only")
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != egressPort {
		p.reject(w, http.StatusForbidden, egressRefusedEvent, "port-not-allowed", r.Host, "port not allowed")
		return
	}
	host = normalizeHost(host)
	if _, ok := p.allow[host]; !ok {
		p.reject(w, http.StatusForbidden, egressRefusedEvent, "host-not-allowed", host, "host not allowed")
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		p.reject(w, http.StatusServiceUnavailable, egressRefusedEvent, "too-many-tunnels", host, "too many tunnels")
		return
	}
	addrs, err := p.target(r.Context(), host)
	if err != nil {
		var denied *deniedAddrError
		if errors.As(err, &denied) {
			p.reject(w, http.StatusForbidden, egressRefusedEvent, "address-not-allowed", host, "address not allowed", "addr", denied.addr.String())
			return
		}
		p.reject(w, http.StatusBadGateway, egressFailedEvent, "resolve-failed", host, "host unreachable", "err", err.Error())
		return
	}
	upstream, err := p.dialAny(r.Context(), addrs)
	if err != nil {
		p.reject(w, http.StatusBadGateway, egressFailedEvent, "dial-failed", host, "host unreachable", "err", err.Error())
		return
	}
	defer func() { _ = upstream.Close() }()
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.reject(w, http.StatusInternalServerError, egressFailedEvent, "cannot-hijack", host, "cannot tunnel")
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		p.logger.Warn(egressFailedEvent, "reason", "hijack-failed", "host", host, "err", err.Error())
		return
	}
	defer func() { _ = client.Close() }()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	p.pipe(client, buf, upstream, host)
}

// deniedAddrError is a resolved address the proxy must not reach.
type deniedAddrError struct{ addr netip.Addr }

func (e *deniedAddrError) Error() string {
	return fmt.Sprintf("address %s is not routable to the internet", e.addr)
}

// internalPrefixes are ranges that pass IsGlobalUnicast and IsPrivate yet reach
// no public API host: carrier-grade NAT, reserved blocks, and the IPv6 forms
// that embed an IPv4 address, plus the documentation and test blocks.
var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// routable reports whether a is a public unicast address, after unmapping an
// IPv4-mapped one.
func routable(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	return !slices.ContainsFunc(internalPrefixes, func(p netip.Prefix) bool { return p.Contains(a) })
}

// target resolves host once and returns its addresses to dial, refusing the host
// outright when any answer is not routable.
func (p *EgressProxy) target(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := p.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolving %s: no addresses", host)
	}
	out := make([]netip.Addr, len(addrs))
	for i, a := range addrs {
		if !routable(a) {
			return nil, &deniedAddrError{addr: a}
		}
		out[i] = a.Unmap()
	}
	return out, nil
}

// dialAny dials port 443 of each address in turn and returns the first connection.
func (p *EgressProxy) dialAny(ctx context.Context, addrs []netip.Addr) (net.Conn, error) {
	var errs []error
	for _, a := range addrs {
		c, err := p.dial(ctx, net.JoinHostPort(a.String(), egressPort))
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// tunnelClock gives a tunnel's idle limit: first until a byte has moved in
// either direction, idle after.
type tunnelClock struct {
	first, idle time.Duration
	moved       atomic.Bool
	// onFirst runs once, when the first byte moves.
	onFirst func()
}

func (c *tunnelClock) timeout() time.Duration {
	if c.moved.Load() {
		return c.idle
	}
	return c.first
}

func (c *tunnelClock) mark() {
	if c.moved.CompareAndSwap(false, true) {
		c.onFirst()
	}
}

// pipe copies both ways until either side closes or goes idle; buffered holds
// bytes the client sent before the hijack completed. An error in either
// direction closes both, and the first idle timeout is logged once.
func (p *EgressProxy) pipe(client net.Conn, buffered io.Reader, upstream net.Conn, host string) {
	clock := &tunnelClock{first: p.firstByte, idle: p.idle}
	// Reads already waiting under the first-byte limit take the idle one.
	clock.onFirst = func() {
		d := time.Now().Add(p.idle)
		_ = client.SetReadDeadline(d)
		_ = upstream.SetReadDeadline(d)
	}
	var wg sync.WaitGroup
	var idleOnce sync.Once
	half := func(dst, src net.Conn, from io.Reader) {
		defer wg.Done()
		_, err := io.Copy(idleWriter{dst, clock}, idleReader{from, src, clock})
		if err == nil {
			closeWrite(dst)
			return
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			idleOnce.Do(func() {
				p.logger.Warn(egressFailedEvent, "reason", "tunnel-idle", "host", host)
			})
		}
		_ = client.Close()
		_ = upstream.Close()
	}
	wg.Add(2)
	go half(upstream, client, buffered)
	go half(client, upstream, upstream)
	wg.Wait()
}

// idleReader reads from r under a deadline on c that each read renews.
type idleReader struct {
	r     io.Reader
	c     net.Conn
	clock *tunnelClock
}

func (i idleReader) Read(b []byte) (int, error) {
	_ = i.c.SetReadDeadline(time.Now().Add(i.clock.timeout()))
	n, err := i.r.Read(b)
	if n > 0 {
		i.clock.mark()
	}
	return n, err
}

// idleWriter writes to c under a deadline that each write renews.
type idleWriter struct {
	c     net.Conn
	clock *tunnelClock
}

func (i idleWriter) Write(b []byte) (int, error) {
	_ = i.c.SetWriteDeadline(time.Now().Add(i.clock.timeout()))
	n, err := i.c.Write(b)
	if n > 0 {
		i.clock.mark()
	}
	return n, err
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
