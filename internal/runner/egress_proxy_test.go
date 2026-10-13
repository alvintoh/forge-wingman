package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

var quietLogger = slog.New(slog.DiscardHandler)

func resolveTo(addrs ...string) func(context.Context, string) ([]netip.Addr, error) {
	return func(context.Context, string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

func TestEgressProxyRefusesWhatIsNotAnAllowedHostOnPort443(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		target  string
		resolve func(context.Context, string) ([]netip.Addr, error)
		want    int
	}{
		{"other host", "CONNECT", "evil.example.com:443", resolveTo("93.184.216.34"), http.StatusForbidden},
		{"metadata address", "CONNECT", "169.254.169.254:443", resolveTo("169.254.169.254"), http.StatusForbidden},
		{"port 80", "CONNECT", "api.example.com:80", resolveTo("93.184.216.34"), http.StatusForbidden},
		{"no port", "CONNECT", "api.example.com", resolveTo("93.184.216.34"), http.StatusForbidden},
		{"non-CONNECT method", "GET", "api.example.com:443", resolveTo("93.184.216.34"), http.StatusMethodNotAllowed},
		{"private address", "CONNECT", "api.example.com:443", resolveTo("10.0.0.5"), http.StatusForbidden},
		{"metadata via allowed name", "CONNECT", "api.example.com:443", resolveTo("93.184.216.34", "169.254.169.254"), http.StatusForbidden},
		{"loopback address", "CONNECT", "api.example.com:443", resolveTo("::ffff:127.0.0.1"), http.StatusForbidden},
		{"carrier-grade NAT", "CONNECT", "api.example.com:443", resolveTo("100.64.0.1"), http.StatusForbidden},
		{"mapped carrier-grade NAT", "CONNECT", "api.example.com:443", resolveTo("::ffff:100.64.0.1"), http.StatusForbidden},
		{"mapped unspecified", "CONNECT", "api.example.com:443", resolveTo("::ffff:0.0.0.0"), http.StatusForbidden},
		{"unspecified", "CONNECT", "api.example.com:443", resolveTo("0.0.0.0"), http.StatusForbidden},
		{"IPv4-compatible loopback", "CONNECT", "api.example.com:443", resolveTo("::127.0.0.1"), http.StatusForbidden},
		{"NAT64 metadata", "CONNECT", "api.example.com:443", resolveTo("64:ff9b::a9fe:a9fe"), http.StatusForbidden},
		{"EC2 IPv6 metadata", "CONNECT", "api.example.com:443", resolveTo("fd00:ec2::254"), http.StatusForbidden},
		{"multicast", "CONNECT", "api.example.com:443", resolveTo("224.0.0.1"), http.StatusForbidden},
		{"literal IP target", "CONNECT", "93.184.216.34:443", resolveTo("93.184.216.34"), http.StatusForbidden},
		{"bracketed IPv6 target", "CONNECT", "[2606:2800:220:1::1]:443", resolveTo("2606:2800:220:1::1"), http.StatusForbidden},
		{"reserved test net", "CONNECT", "api.example.com:443", resolveTo("192.0.0.1"), http.StatusForbidden},
		{"benchmark net", "CONNECT", "api.example.com:443", resolveTo("198.18.0.1"), http.StatusForbidden},
		{"reserved class E", "CONNECT", "api.example.com:443", resolveTo("240.0.0.1"), http.StatusForbidden},
		{"6to4 metadata", "CONNECT", "api.example.com:443", resolveTo("2002:a9fe:a9fe::1"), http.StatusForbidden},
		{"site-local", "CONNECT", "api.example.com:443", resolveTo("fec0::1"), http.StatusForbidden},
		{"zero net", "CONNECT", "api.example.com:443", resolveTo("0.1.2.3"), http.StatusForbidden},
		{"zero net top", "CONNECT", "api.example.com:443", resolveTo("0.255.255.255"), http.StatusForbidden},
		{"documentation 1", "CONNECT", "api.example.com:443", resolveTo("192.0.2.1"), http.StatusForbidden},
		{"documentation 2", "CONNECT", "api.example.com:443", resolveTo("198.51.100.1"), http.StatusForbidden},
		{"documentation 3", "CONNECT", "api.example.com:443", resolveTo("203.0.113.1"), http.StatusForbidden},
		{"IPv6 documentation", "CONNECT", "api.example.com:443", resolveTo("2001:db8::1"), http.StatusForbidden},
		{"Teredo", "CONNECT", "api.example.com:443", resolveTo("2001::1"), http.StatusForbidden},
		{"IPv4-translated metadata", "CONNECT", "api.example.com:443", resolveTo("::ffff:0:a9fe:a9fe"), http.StatusForbidden},
		{"local-use NAT64", "CONNECT", "api.example.com:443", resolveTo("64:ff9b:1::1"), http.StatusForbidden},
		{"no addresses", "CONNECT", "api.example.com:443", resolveTo(), http.StatusBadGateway},
		{"unresolvable", "CONNECT", "api.example.com:443", func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewEgressProxy([]string{"api.example.com"}, quietLogger)
			p.resolve = tt.resolve
			p.dial = func(context.Context, string) (net.Conn, error) {
				t.Error("proxy dialled a refused target")
				return nil, errors.New("refused")
			}
			req := httptest.NewRequest(tt.method, "http://"+tt.target, nil)
			req.Host = tt.target
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestEgressProxyTunnelsAnAllowedHostToItsResolvedAddress(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		line, _ := bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("echo:" + line))
	}()

	var dialled string
	p := NewEgressProxy([]string{"API.example.com"}, quietLogger)
	p.resolve = resolveTo("93.184.216.34")
	p.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		dialled = addr
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Addr().String())
	}
	srv := httptest.NewServer(p)
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("CONNECT API.Example.COM.:443 HTTP/1.1\r\nHost: API.Example.COM.:443\r\n\r\nhello\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("status line %q, want 200", status)
	}
	_, _ = r.ReadString('\n')
	got, _ := r.ReadString('\n')
	if got != "echo:hello\n" {
		t.Fatalf("tunnel returned %q, want the upstream's echo", got)
	}
	if dialled != "93.184.216.34:443" {
		t.Fatalf("dialled %q, want the resolved address on 443", dialled)
	}
}

func TestEgressProxyDialsTheNextAddressWhenTheFirstIsUnreachable(t *testing.T) {
	var dialled []string
	p := NewEgressProxy([]string{"api.example.com"}, quietLogger)
	p.dial = func(_ context.Context, addr string) (net.Conn, error) {
		dialled = append(dialled, addr)
		if strings.HasPrefix(addr, "[") {
			return nil, errors.New("network unreachable")
		}
		c, _ := net.Pipe()
		return c, nil
	}
	conn, err := p.dialAny(context.Background(), []netip.Addr{netip.MustParseAddr("2606:2800:220:1::1"), netip.MustParseAddr("93.184.216.34")})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if want := []string{"[2606:2800:220:1::1]:443", "93.184.216.34:443"}; !slices.Equal(dialled, want) {
		t.Fatalf("dialled %v, want %v", dialled, want)
	}
}

func TestEgressProxyDialsAMappedAddressAsIPv4(t *testing.T) {
	p := NewEgressProxy([]string{"api.example.com"}, quietLogger)
	p.resolve = resolveTo("::ffff:93.184.216.34")
	addrs, err := p.target(context.Background(), "api.example.com")
	if err != nil || len(addrs) != 1 || addrs[0].Is4In6() {
		t.Fatalf("target = %v, %v; want the unmapped IPv4 address", addrs, err)
	}
}

func TestEgressProxyAnswers502WhenNoResolvedAddressAccepts(t *testing.T) {
	p := NewEgressProxy([]string{"api.example.com"}, quietLogger)
	p.resolve = resolveTo("93.184.216.34", "93.184.216.35")
	p.dial = func(context.Context, string) (net.Conn, error) { return nil, errors.New("connection refused") }
	req := httptest.NewRequest("CONNECT", "http://api.example.com:443", nil)
	req.Host = "api.example.com:443"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

// logBuffer is a slog sink safe to read while tunnel goroutines write to it.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func loggedProxy(hosts ...string) (*EgressProxy, *logBuffer) {
	buf := &logBuffer{}
	return NewEgressProxy(hosts, slog.New(slog.NewJSONHandler(buf, nil))), buf
}

// hijackFails is a ResponseWriter whose Hijack fails.
type hijackFails struct{ *httptest.ResponseRecorder }

func (hijackFails) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack refused")
}

func TestEgressProxyLogsEachRefusalAndFailureWithTheHostAsItsOwnAttr(t *testing.T) {
	tests := []struct {
		name, method, target string
		resolve              func(context.Context, string) ([]netip.Addr, error)
		dial                 func(context.Context, string) (net.Conn, error)
		writer               func() http.ResponseWriter
		want                 []string
	}{
		{name: "method", method: "GET", target: "api.example.com:443", want: []string{`"msg":"egressRefused"`, `"reason":"method-not-connect"`, `"host":"api.example.com:443"`}},
		{name: "port", method: "CONNECT", target: "api.example.com:80", want: []string{`"msg":"egressRefused"`, `"reason":"port-not-allowed"`}},
		{name: "host", method: "CONNECT", target: "Evil.example.com:443", want: []string{`"msg":"egressRefused"`, `"reason":"host-not-allowed"`, `"host":"evil.example.com"`}},
		{name: "address", method: "CONNECT", target: "api.example.com:443", resolve: resolveTo("169.254.169.254"), want: []string{`"msg":"egressRefused"`, `"reason":"address-not-allowed"`, `"addr":"169.254.169.254"`}},
		{name: "resolve", method: "CONNECT", target: "api.example.com:443", resolve: resolveTo(), want: []string{`"msg":"egressFailed"`, `"reason":"resolve-failed"`}},
		{name: "dial", method: "CONNECT", target: "api.example.com:443", resolve: resolveTo("93.184.216.34"), dial: func(context.Context, string) (net.Conn, error) { return nil, errors.New("refused") }, want: []string{`"msg":"egressFailed"`, `"reason":"dial-failed"`}},
		{name: "no hijacker", method: "CONNECT", target: "api.example.com:443", resolve: resolveTo("93.184.216.34"), dial: pipeDial, want: []string{`"msg":"egressFailed"`, `"reason":"cannot-hijack"`}},
		{name: "hijack fails", method: "CONNECT", target: "api.example.com:443", resolve: resolveTo("93.184.216.34"), dial: pipeDial, writer: func() http.ResponseWriter { return hijackFails{httptest.NewRecorder()} }, want: []string{`"msg":"egressFailed"`, `"reason":"hijack-failed"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, logs := loggedProxy("api.example.com")
			if tt.resolve != nil {
				p.resolve = tt.resolve
			}
			if tt.dial != nil {
				p.dial = tt.dial
			}
			req := httptest.NewRequest(tt.method, "http://"+tt.target, nil)
			req.Host = tt.target
			var w http.ResponseWriter = httptest.NewRecorder()
			if tt.writer != nil {
				w = tt.writer()
			}
			p.ServeHTTP(w, req)
			wantCounts := `"refused":1,"failed":0`
			if strings.Contains(tt.want[0], "egressFailed") {
				wantCounts = `"refused":0,"failed":1`
			}
			if got := strings.Count(logs.String(), "\n"); got != 1 {
				t.Fatalf("%d log lines %q, want one", got, logs.String())
			}
			for _, frag := range tt.want {
				if !strings.Contains(logs.String(), frag) {
					t.Errorf("log %q lacks %s", logs.String(), frag)
				}
			}
			p.LogSummary()
			if !strings.Contains(logs.String(), wantCounts) {
				t.Errorf("summary %q lacks %s", logs.String(), wantCounts)
			}
		})
	}
}

func pipeDial(context.Context, string) (net.Conn, error) {
	c, _ := net.Pipe()
	return c, nil
}

// connectThrough opens a tunnel request to the proxy at srv and returns the connection and the status line.
func connectThrough(t *testing.T, srv *httptest.Server) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte("CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	status, _ := r.ReadString('\n')
	for {
		if line, err := r.ReadString('\n'); err != nil || line == "\r\n" {
			break
		}
	}
	return conn, r, status
}

// silentUpstream accepts connections and never writes, so a tunnel to it goes idle.
func silentUpstream(t *testing.T) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
}

func TestEgressProxyClosesATunnelThatMovesNoBytes(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	p.dial = silentUpstream(t)
	p.idle, p.firstByte = 150*time.Millisecond, 150*time.Millisecond
	srv := httptest.NewServer(p)
	defer srv.Close()

	conn, r, status := connectThrough(t, srv)
	if !strings.Contains(status, "200") {
		t.Fatalf("status %q, want 200", status)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read on a stalled tunnel = %v, want the proxy to have closed it", err)
	}
	if !strings.Contains(logs.String(), `"reason":"tunnel-idle"`) || strings.Count(logs.String(), "tunnel-idle") != 1 {
		t.Fatalf("log %q, want one tunnel-idle line", logs.String())
	}
	p.LogSummary()
	if !strings.Contains(logs.String(), `"reasons":{"tunnel-idle":1}`) {
		t.Fatalf("summary %q does not count the idle tunnel", logs.String())
	}
}

func TestEgressProxyKeepsATunnelOpenWhileBytesMove(t *testing.T) {
	p, _ := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	echo := echoUpstream(t)
	p.dial = echo
	p.idle = 300 * time.Millisecond
	srv := httptest.NewServer(p)
	defer srv.Close()

	conn, r, _ := connectThrough(t, srv)
	for range 4 {
		time.Sleep(150 * time.Millisecond)
		if _, err := conn.Write([]byte("ping\n")); err != nil {
			t.Fatal(err)
		}
		if line, err := r.ReadString('\n'); err != nil || line != "ping\n" {
			t.Fatalf("echo = %q, %v; the tunnel closed while bytes moved", line, err)
		}
	}
}

// echoUpstream accepts connections and echoes every byte back.
func echoUpstream(t *testing.T) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
}

func TestEgressProxyRefusesATunnelBeyondTheCapAndFreesTheSlotOnClose(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	p.dial = silentUpstream(t)
	p.slots = make(chan struct{}, 1)
	p.idle, p.firstByte = time.Second, time.Second
	srv := httptest.NewServer(p)
	defer srv.Close()

	first, _, status := connectThrough(t, srv)
	if !strings.Contains(status, "200") {
		t.Fatalf("first tunnel %q, want 200", status)
	}
	_, _, status = connectThrough(t, srv)
	if !strings.Contains(status, "503") {
		t.Fatalf("tunnel beyond the cap %q, want 503", status)
	}
	if !strings.Contains(logs.String(), `"reason":"too-many-tunnels"`) {
		t.Fatalf("log %q, want a too-many-tunnels line", logs.String())
	}
	_ = first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, status = connectThrough(t, srv)
		if strings.Contains(status, "200") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed tunnel never freed its slot; last status %q", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEgressProxyClosesATunnelWhoseClientStopsReadingWhileItKeepsWriting(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.CopyN(c, zeroReader{}, 64<<20)
	}()
	p.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
	p.idle = 200 * time.Millisecond
	srv := httptest.NewServer(p)
	defer srv.Close()

	conn, _, _ := connectThrough(t, srv)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				_, _ = conn.Write([]byte("x"))
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), `"reason":"tunnel-idle"`) {
		if time.Now().After(deadline) {
			t.Fatal("a tunnel whose client never reads was not closed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// zeroReader reads zero bytes forever.
type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestEgressProxyDoesNotCallAnAbandonedTunnelIdle(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.CopyN(c, zeroReader{}, 64<<20)
	}()
	p.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
	srv := httptest.NewServer(p)
	conn, _, _ := connectThrough(t, srv)
	_ = conn.(*net.TCPConn).SetLinger(0)
	_ = conn.Close()
	time.Sleep(300 * time.Millisecond)
	srv.Close()
	if strings.Contains(logs.String(), "tunnel-idle") {
		t.Fatalf("log %q calls a client that went away idle", logs.String())
	}
}

func TestEgressProxyGivesASilentTunnelTheShortFirstByteLimitOnly(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	p.dial = silentUpstream(t)
	p.idle, p.firstByte = time.Minute, 150*time.Millisecond
	srv := httptest.NewServer(p)
	defer srv.Close()

	conn, r, _ := connectThrough(t, srv)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read on a tunnel that never moved a byte = %v, want the proxy to have closed it", err)
	}
	if !strings.Contains(logs.String(), `"reason":"tunnel-idle"`) {
		t.Fatalf("log %q, want a tunnel-idle line", logs.String())
	}
}

func TestEgressProxyLetsATunnelWaitLongerOnceBytesHaveMoved(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		line, _ := bufio.NewReader(c).ReadString('\n')
		time.Sleep(600 * time.Millisecond)
		_, _ = c.Write([]byte("late:" + line))
	}()
	p, _ := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	p.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
	p.idle, p.firstByte = 5*time.Second, 200*time.Millisecond
	srv := httptest.NewServer(p)
	defer srv.Close()

	conn, r, _ := connectThrough(t, srv)
	if _, err := conn.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || line != "late:one\n" {
		t.Fatalf("reply %q, %v; the tunnel kept the first-byte limit after bytes moved", line, err)
	}
}

func TestEgressProxyLogsALongMultiByteHostCutOnACharacterBoundary(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	host := strings.Repeat("é", 400)
	req := httptest.NewRequest("CONNECT", "http://api.example.com:443", nil)
	req.Host = host + ":443"
	p.ServeHTTP(httptest.NewRecorder(), req)

	var line struct{ Host string }
	if err := json.Unmarshal([]byte(logs.String()), &line); err != nil {
		t.Fatalf("log %q is not JSON: %v", logs.String(), err)
	}
	if !utf8.ValidString(line.Host) || len(line.Host) > egressLoggedHostBytes+len("…") || !strings.HasPrefix(line.Host, "éé") {
		t.Fatalf("logged host is %d bytes (valid %v), want a valid cut of at most %d", len(line.Host), utf8.ValidString(line.Host), egressLoggedHostBytes+len("…"))
	}
}

func TestEgressProxySummaryCountsByReasonAndNamesTheFirstDistinctRefusedHosts(t *testing.T) {
	p, logs := loggedProxy("api.example.com")
	p.resolve = resolveTo("93.184.216.34")
	p.dial = func(context.Context, string) (net.Conn, error) { return nil, errors.New("refused") }
	serve := func(target string) {
		req := httptest.NewRequest("CONNECT", "http://api.example.com:443", nil)
		req.Host = target
		p.ServeHTTP(httptest.NewRecorder(), req)
	}
	for i := range egressSummaryHosts + 5 {
		serve("h" + strconv.Itoa(i) + ".example:443")
		if i == 1 {
			serve("h0.example:443")
		}
	}
	serve("api.example.com:80")
	serve("api.example.com:443")

	p.LogSummary()
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	var sum struct {
		Msg               string
		Refused, Failed   int
		Reasons           map[string]int
		FirstRefusedHosts []string
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Msg != egressSummaryEvent || sum.Refused != egressSummaryHosts+5+1+1 || sum.Failed != 1 {
		t.Fatalf("summary %+v, want %d refused and 1 failed", sum, egressSummaryHosts+7)
	}
	if sum.Reasons["host-not-allowed"] != egressSummaryHosts+6 || sum.Reasons["port-not-allowed"] != 1 || sum.Reasons["dial-failed"] != 1 {
		t.Errorf("reasons %v", sum.Reasons)
	}
	if len(sum.FirstRefusedHosts) != egressSummaryHosts || sum.FirstRefusedHosts[0] != "h0.example" || sum.FirstRefusedHosts[1] != "h1.example" || sum.FirstRefusedHosts[2] != "h2.example" {
		t.Errorf("first hosts %v, want the first %d distinct in order", sum.FirstRefusedHosts, egressSummaryHosts)
	}
}
