package linear

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// oauthServer mints minted-1, minted-2, … and records what it revoked. A
// non-zero mintStatus or revokeStatus fails that endpoint.
type oauthServer struct {
	mintStatus, revokeStatus int

	mu      sync.Mutex
	forms   []map[string]string
	revoked []string
}

func (o *oauthServer) start(t *testing.T) Credentials {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the form: %v", err)
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		switch r.URL.Path {
		case "/token":
			if o.mintStatus != 0 {
				w.WriteHeader(o.mintStatus)
				return
			}
			form := map[string]string{}
			for k := range r.PostForm {
				form[k] = r.PostForm.Get(k)
			}
			o.forms = append(o.forms, form)
			_, _ = fmt.Fprintf(w, `{"access_token":"minted-%d","token_type":"Bearer"}`, len(o.forms))
		case "/revoke":
			if o.revokeStatus != 0 {
				w.WriteHeader(o.revokeStatus)
				return
			}
			o.revoked = append(o.revoked, r.PostForm.Get("token"))
		}
	}))
	t.Cleanup(srv.Close)
	return Credentials{ClientID: "client-1", ClientSecret: "secret-1",
		TokenURL: srv.URL + "/token", RevokeURL: srv.URL + "/revoke", HTTP: srv.Client()}
}

func (o *oauthServer) mints() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.forms)
}

func TestMintPostsTheClientCredentialsGrant(t *testing.T) {
	o := &oauthServer{}
	token, err := o.start(t).mint(context.Background())
	if err != nil || token != "minted-1" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	want := map[string]string{"grant_type": "client_credentials", "client_id": "client-1",
		"client_secret": "secret-1", "scope": "read,write,app:assignable"}
	if got := o.forms[0]; !maps.Equal(got, want) {
		t.Fatalf("form = %v", got)
	}
}

func TestMintFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reply  string
		creds  func(Credentials) Credentials
	}{
		{name: "refused", status: 401, reply: `{"error":"invalid_client"}`},
		{name: "no access token", status: 200, reply: `{}`},
		{name: "no client id", creds: func(c Credentials) Credentials { c.ClientID = ""; return c }},
		{name: "no client secret", creds: func(c Credentials) Credentials { c.ClientSecret = ""; return c }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.reply)
			}))
			t.Cleanup(srv.Close)
			creds := Credentials{ClientID: "client-1", ClientSecret: "secret-1", TokenURL: srv.URL, HTTP: srv.Client()}
			if tc.creds != nil {
				creds = tc.creds(creds)
			}
			if token, err := creds.mint(context.Background()); err == nil {
				t.Fatalf("minted %q", token)
			}
			if tc.creds != nil && calls != 0 {
				t.Fatalf("asked Linear %d times without credentials", calls)
			}
		})
	}
}

func TestRevokePostsTheToken(t *testing.T) {
	o := &oauthServer{}
	if err := o.start(t).revoke(context.Background(), "minted-9"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.revoked, []string{"minted-9"}) {
		t.Fatalf("revoked %v", o.revoked)
	}
}

func TestRevokeReportsARefusal(t *testing.T) {
	o := &oauthServer{revokeStatus: 400}
	if err := o.start(t).revoke(context.Background(), "minted-9"); err == nil {
		t.Fatal("revoked")
	}
}

func TestPollSourceMintsOnceAndRefreshesOnce(t *testing.T) {
	ctx := context.Background()
	o := &oauthServer{}
	src := NewPollSource(o.start(t))
	first, _ := src.Token(ctx)
	again, _ := src.Token(ctx)
	if first != "minted-1" || again != first {
		t.Fatalf("tokens %q, %q", first, again)
	}
	fresh, err := src.Refresh(ctx, first)
	if err != nil || fresh != "minted-2" {
		t.Fatalf("refreshed to %q, %v", fresh, err)
	}
	if got, err := src.Refresh(ctx, first); err != nil || got != fresh {
		t.Fatalf("a refresh of a replaced token gave %q, %v", got, err)
	}
	if _, err := src.Refresh(ctx, fresh); !errors.Is(err, errRefreshSpent) {
		t.Fatalf("err = %v, want errRefreshSpent", err)
	}
	if o.mints() != 2 {
		t.Fatalf("minted %d tokens", o.mints())
	}
}

func TestPollSourceCloseRevokesEveryTokenItMinted(t *testing.T) {
	ctx := context.Background()
	o := &oauthServer{}
	src := NewPollSource(o.start(t))
	first, _ := src.Token(ctx)
	_, _ = src.Refresh(ctx, first)
	if err := src.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.revoked, []string{"minted-1", "minted-2"}) {
		t.Fatalf("revoked %v", o.revoked)
	}
	if token, _ := src.Token(ctx); token != "minted-3" {
		t.Fatalf("handed out %q after closing", token)
	}
}

func TestPollSourceCloseReportsAFailedRevoke(t *testing.T) {
	o := &oauthServer{revokeStatus: 500}
	src := NewPollSource(o.start(t))
	_, _ = src.Token(context.Background())
	if err := src.Close(context.Background()); err == nil {
		t.Fatal("closed with the revoke refused")
	}
}

func TestCachedSourceRemintsAtMostOncePerWindow(t *testing.T) {
	ctx := context.Background()
	o := &oauthServer{}
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	src := NewCachedSource(o.start(t), func() time.Time { return now })
	first, _ := src.Token(ctx)
	if again, _ := src.Token(ctx); again != first {
		t.Fatalf("token changed to %q", again)
	}
	second, err := src.Refresh(ctx, first)
	if err != nil || second != "minted-2" {
		t.Fatalf("refreshed to %q, %v", second, err)
	}
	if got, _ := src.Refresh(ctx, first); got != second {
		t.Fatalf("a refresh of a replaced token gave %q", got)
	}
	now = now.Add(remintEvery - time.Second)
	if _, err := src.Refresh(ctx, second); err == nil {
		t.Fatal("re-minted inside the window")
	}
	now = now.Add(time.Second)
	if third, err := src.Refresh(ctx, second); err != nil || third != "minted-3" {
		t.Fatalf("refreshed to %q, %v", third, err)
	}
}

func TestCachedSourceRestsAfterAFailedMint(t *testing.T) {
	o := &oauthServer{mintStatus: 500}
	src := NewCachedSource(o.start(t), func() time.Time { return time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC) })
	_, first := src.Token(context.Background())
	o.mintStatus = 0
	if token, err := src.Token(context.Background()); first == nil || err == nil || o.mints() != 0 {
		t.Fatalf("minted %q after a failure (%v)", token, first)
	}
}

func TestCachedSourceCloseRevokesTheHeldToken(t *testing.T) {
	o := &oauthServer{}
	src := NewCachedSource(o.start(t), time.Now)
	_, _ = src.Token(context.Background())
	for range 2 {
		if err := src.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(o.revoked, []string{"minted-1"}) {
		t.Fatalf("revoked %v", o.revoked)
	}
	if token, err := src.Refresh(context.Background(), "minted-1"); err != nil || token != "minted-2" {
		t.Fatalf("refreshed to %q, %v after closing", token, err)
	}
}

func TestCachedSourceMintsOnceForConcurrentRefusals(t *testing.T) {
	ctx := context.Background()
	o := &oauthServer{}
	src := NewCachedSource(o.start(t), time.Now)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			token, err := src.Token(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = src.Refresh(ctx, token)
		})
	}
	wg.Wait()
	if o.mints() != 2 {
		t.Fatalf("minted %d tokens", o.mints())
	}
}

// scriptedSource hands out token and, on refresh, fresh or refreshErr.
type scriptedSource struct {
	token, fresh string
	tokenErr     error
	refreshErr   error
	refreshes    int
}

func (s *scriptedSource) Token(context.Context) (string, error) { return s.token, s.tokenErr }

func (s *scriptedSource) Refresh(context.Context, string) (string, error) {
	s.refreshes++
	return s.fresh, s.refreshErr
}

func TestDoRefreshing(t *testing.T) {
	refused := fmt.Errorf("%w: 401", ErrUnauthenticated)
	other := errors.New("no access")
	spent := errors.New("spent")
	for _, tc := range []struct {
		name      string
		src       scriptedSource
		answers   map[string]error
		wantSent  string
		wantErr   error
		refreshes int
	}{
		{name: "accepted", src: scriptedSource{token: "t1"}, wantSent: "t1"},
		{name: "refused then refreshed", src: scriptedSource{token: "t1", fresh: "t2"},
			answers: map[string]error{"t1": refused}, wantSent: "t1,t2", refreshes: 1},
		{name: "another failure", src: scriptedSource{token: "t1"},
			answers: map[string]error{"t1": other}, wantSent: "t1", wantErr: other},
		{name: "refresh fails", src: scriptedSource{token: "t1", refreshErr: spent},
			answers: map[string]error{"t1": refused}, wantSent: "t1", wantErr: spent, refreshes: 1},
		{name: "no token", src: scriptedSource{tokenErr: spent}, wantErr: spent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []string
			err := DoRefreshing(context.Background(), &tc.src, func(token string) error {
				sent = append(sent, token)
				return tc.answers[token]
			})
			if (tc.wantErr == nil) != (err == nil) || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if strings.Join(sent, ",") != tc.wantSent || tc.src.refreshes != tc.refreshes {
				t.Fatalf("sent %v after %d refreshes", sent, tc.src.refreshes)
			}
		})
	}
}

// closer is a source whose Close records the context it was given.
type closer struct {
	err    error
	ctxErr error
}

func (c *closer) Close(ctx context.Context) error {
	c.ctxErr = ctx.Err()
	return c.err
}

func TestRevokeAllOutlivesTheCallersContextAndOnlyWarns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var logs bytes.Buffer
	c := &closer{err: errors.New("revoke refused")}
	RevokeAll(ctx, c, slog.New(slog.NewTextHandler(&logs, nil)))
	if c.ctxErr != nil {
		t.Fatalf("closed on a context already done: %v", c.ctxErr)
	}
	if !strings.Contains(logs.String(), "level=WARN msg=linearRevokeFailed") {
		t.Fatalf("logged %q", logs.String())
	}
}
