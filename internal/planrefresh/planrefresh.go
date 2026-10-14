// Package planrefresh reads what a provider plan's own sources say about it — the
// vendor's pages and models endpoint, the Coding leaderboard's scores and the
// tokenplans free feed — and turns one read of them into the dated facts document
// the owner reads.
//
// A read that fails is never an error here: the source is recorded as
// could-not-read, with the date it was last read and the figures it last answered
// with, so the refresh always produces a document and the owner can tell a source
// that went quiet from one that moved.
package planrefresh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// dateLayout is the yyyy-mm-dd shape a read date and a leaderboard's own date are
// stored in.
const dateLayout = "2006-01-02"

const (
	// requestTimeout bounds one request a refresh makes, so a source that hangs
	// cannot hold the weekly read open.
	requestTimeout = 30 * time.Second
	// maxRedirects is how many same-host redirects a read follows.
	maxRedirects = 3

	// maxModels, maxPerks and maxFieldBytes cap what a source's own lists and
	// strings are stored as, so a source cannot carry a facts document past what
	// the store accepts.
	maxModels     = 500
	maxPerks      = 50
	maxFieldBytes = 200
	// maxNoteBytes caps the reason a source could not be read.
	maxNoteBytes = 500
)

// errInternalAddress is why a dial was refused: the address a name resolved to is
// one a public source never lives at.
var errInternalAddress = errors.New("refusing to connect to a loopback, link-local, private, unspecified or multicast address")

// NewClient returns the client every refresh read goes through. It follows a
// redirect only to the same host over https, and refuses to connect to an address
// that is loopback, link-local, private, unspecified or multicast, checked on the
// address actually dialled so a name that resolves inward is refused too.
func NewClient() *http.Client { return newClient(true) }

// newClient builds NewClient's client; guard false skips the address check, so a
// test can read from a server on the loopback.
func newClient(guard bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if guard {
		dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refuseInternal}
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{Timeout: requestTimeout, Transport: transport, CheckRedirect: sameHostHTTPS}
}

// refuseInternal is a dialer's Control: it runs on the resolved ip:port about to
// be connected to.
func refuseInternal(_, address string, _ syscall.RawConn) error {
	addr, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("dialling %q: %w", address, err)
	}
	ip := addr.Addr()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return errInternalAddress
	}
	return nil
}

// sameHostHTTPS is the client's CheckRedirect: a redirect may stay on the host it
// started from and on https, a few times, and nothing else.
func sameHostHTTPS(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s, which leaves https", req.URL.Redacted())
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("refusing a redirect from %s to %s, which changes host", via[0].URL.Host, req.URL.Host)
	}
	return nil
}

// capList caps a source's own list at n entries, each at maxFieldBytes.
func capList(items []string, n int) []string {
	if len(items) > n {
		items = items[:n]
	}
	capped := make([]string, len(items))
	for i, item := range items {
		capped[i] = providers.CapText(item, maxFieldBytes)
	}
	return capped
}

// readBody reads url, refusing any status but 200 and any body over limit bytes,
// so one source answering with an error page or a flood is recorded as unread
// rather than parsed.
func readBody(ctx context.Context, client *http.Client, url string, limit int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", url, err)
	}
	if client == nil {
		client = NewClient()
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%s answered over %d bytes", url, limit)
	}
	return body, nil
}
