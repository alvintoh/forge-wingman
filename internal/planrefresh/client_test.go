package planrefresh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestClientRefusesAnInternalAddress asserts the production client will not
// connect to a loopback server, so a source naming an internal address is read as
// unreadable.
func TestClientRefusesAnInternalAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the guarded client reached an internal server")
	}))
	t.Cleanup(srv.Close)

	_, err := readBody(context.Background(), NewClient(), srv.URL, 1024)
	if err == nil || !strings.Contains(err.Error(), errInternalAddress.Error()) {
		t.Fatalf("err = %v, want the internal address refused", err)
	}
}

func TestRefuseInternalAddresses(t *testing.T) {
	for _, tt := range []struct {
		address string
		refused bool
	}{
		{"127.0.0.1:443", true},
		{"[::1]:443", true},
		{"169.254.169.254:80", true},
		{"[fe80::1]:443", true},
		{"10.0.0.5:443", true},
		{"172.16.0.1:443", true},
		{"192.168.1.1:443", true},
		{"[fc00::1]:443", true},
		{"0.0.0.0:443", true},
		{"224.0.0.1:443", true},
		{"[::ffff:127.0.0.1]:443", true},
		{"93.184.216.34:443", false},
		{"[2606:2800:220:1:248:1893:25c8:1946]:443", false},
	} {
		if err := refuseInternal("tcp", tt.address, nil); (err != nil) != tt.refused {
			t.Errorf("%s: err = %v, want refused %v", tt.address, err, tt.refused)
		}
	}
}

// TestClientRefusesARedirectToAnotherHost asserts a redirect that leaves the host
// the read started on is refused, and the target is never requested.
func TestClientRefusesARedirectToAnotherHost(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect's target was requested")
	}))
	t.Cleanup(other.Close)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	// The loopback servers are reached through the unguarded client, trusting the
	// test servers' certificate; the redirect rule is production's.
	client := newClient(false)
	client.Transport.(*http.Transport).TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig
	_, err := readBody(context.Background(), client, origin.URL, 1024)
	if err == nil || !strings.Contains(err.Error(), "changes host") {
		t.Fatalf("err = %v, want the redirect to another host refused", err)
	}
}

// TestRedirectRules asserts what a redirect may do: stay on the host and on
// https, a few times, and nothing else.
func TestRedirectRules(t *testing.T) {
	at := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}
	origin := []*http.Request{at("https://vendor.example/pricing")}
	chain := func(n int) []*http.Request {
		via := make([]*http.Request, n)
		for i := range via {
			via[i] = origin[0]
		}
		return via
	}
	for _, tt := range []struct {
		name string
		to   string
		via  []*http.Request
		want string
	}{
		{"same host over https", "https://vendor.example/plans", origin, ""},
		{"https to http", "http://vendor.example/pricing", origin, "leaves https"},
		{"another host", "https://other.example/pricing", origin, "changes host"},
		{"too many", "https://vendor.example/a", chain(maxRedirects + 1), "redirects"},
		{"the last one allowed", "https://vendor.example/a", chain(maxRedirects), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := sameHostHTTPS(at(tt.to), tt.via)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("err = %v, want the redirect followed", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("err = %v, want it to say %q", err, tt.want)
			}
		})
	}
}

// TestReadBodyRefusesABodyOverTheLimit asserts a body one byte over the cap is
// refused and one at the cap is read.
func TestReadBodyRefusesABodyOverTheLimit(t *testing.T) {
	srv := bodyServer(t, http.StatusOK, strings.Repeat("x", 11))
	if _, err := readBody(context.Background(), srv.Client(), srv.URL, 10); err == nil || !strings.Contains(err.Error(), "over 10 bytes") {
		t.Fatalf("err = %v, want a body over the limit refused", err)
	}
	if body, err := readBody(context.Background(), srv.Client(), srv.URL, 11); err != nil || len(body) != 11 {
		t.Fatalf("read %d bytes, err %v, want a body at the limit read", len(body), err)
	}
}
