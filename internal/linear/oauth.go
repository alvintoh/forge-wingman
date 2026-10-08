package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// scope is the one scope every token is minted with: minting with another
	// revokes every token the app holds.
	scope            = "read,write,app:assignable"
	defaultTokenURL  = "https://api.linear.app/oauth/token"
	defaultRevokeURL = "https://api.linear.app/oauth/revoke"
	maxOAuthReply    = 64 << 10
	remintEvery      = 30 * time.Second
	revokeTimeout    = 10 * time.Second
)

// errRefreshSpent reports a second refresh asked of a PollSource.
var errRefreshSpent = errors.New("linear: the poll's one token refresh is spent")

// Credentials are the app's client credentials, which mint and revoke its tokens.
type Credentials struct {
	ClientID     string
	ClientSecret string
	// TokenURL overrides the mint address; empty means Linear's own.
	TokenURL string
	// RevokeURL overrides the revoke address; empty means Linear's own.
	RevokeURL string
	// HTTP is the client requests go through; nil means http.DefaultClient.
	HTTP *http.Client
}

// mint exchanges the credentials for a new access token carrying scope.
func (c Credentials) mint(ctx context.Context) (string, error) {
	if c.ClientID == "" || c.ClientSecret == "" {
		return "", errors.New("minting a Linear token: no client credentials")
	}
	raw, err := c.post(ctx, c.TokenURL, defaultTokenURL, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"scope":         {scope},
	})
	if err != nil {
		return "", fmt.Errorf("minting a Linear token: %w", err)
	}
	var reply struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("minting a Linear token: decoding the reply: %w", err)
	}
	if reply.AccessToken == "" {
		return "", errors.New("minting a Linear token: the reply carries no access token")
	}
	return reply.AccessToken, nil
}

// revoke ends a token's validity.
func (c Credentials) revoke(ctx context.Context, token string) error {
	if _, err := c.post(ctx, c.RevokeURL, defaultRevokeURL, url.Values{"token": {token}}); err != nil {
		return fmt.Errorf("revoking a Linear token: %w", err)
	}
	return nil
}

func (c Credentials) post(ctx context.Context, endpoint, fallback string, form url.Values) ([]byte, error) {
	if endpoint == "" {
		endpoint = fallback
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxOAuthReply))
	if err != nil {
		return nil, fmt.Errorf("reading the reply: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Linear API answered %s: %s", res.Status, snippet(raw))
	}
	return raw, nil
}

// TokenSource hands out the token Linear calls carry.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Refresh returns a successor to stale, a token Linear refused.
	Refresh(ctx context.Context, stale string) (string, error)
}

// DoRefreshing runs call with the source's token and, if Linear refused it,
// once more with a refreshed one.
func DoRefreshing(ctx context.Context, src TokenSource, call func(token string) error) error {
	token, err := src.Token(ctx)
	if err != nil {
		return err
	}
	err = call(token)
	if !errors.Is(err, ErrUnauthenticated) {
		return err
	}
	fresh, refreshErr := src.Refresh(ctx, token)
	if refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
	return call(fresh)
}

// PollSource mints a token on first use and at most one successor, for the
// span of one poll.
type PollSource struct {
	creds Credentials

	mu        sync.Mutex
	current   string
	refreshed bool
	minted    []string
}

// NewPollSource mints with creds.
func NewPollSource(creds Credentials) *PollSource {
	return &PollSource{creds: creds}
}

// Token returns the poll's token, minting it on first use.
func (p *PollSource) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != "" {
		return p.current, nil
	}
	return p.mint(ctx)
}

// Refresh mints a successor to stale once per poll and fails with
// errRefreshSpent after that.
func (p *PollSource) Refresh(ctx context.Context, stale string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != stale {
		return p.current, nil
	}
	if p.refreshed {
		return "", errRefreshSpent
	}
	p.refreshed = true
	return p.mint(ctx)
}

func (p *PollSource) mint(ctx context.Context) (string, error) {
	token, err := p.creds.mint(ctx)
	if err != nil {
		return "", err
	}
	p.current = token
	p.minted = append(p.minted, token)
	return token, nil
}

// Close revokes every token the source minted and returns the revocations that failed.
func (p *PollSource) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	for _, token := range p.minted {
		errs = append(errs, p.creds.revoke(ctx, token))
	}
	p.minted, p.current = nil, ""
	return errors.Join(errs...)
}

// CachedSource holds one token for a long-lived process, minting it on first
// use and again when Linear refuses it. After a refresh or a failed mint it
// mints nothing for remintEvery.
type CachedSource struct {
	creds Credentials
	now   func() time.Time

	mu      sync.Mutex
	current string
	resting time.Time
}

// NewCachedSource mints with creds; now is the clock the re-mint limit reads.
func NewCachedSource(creds Credentials, now func() time.Time) *CachedSource {
	return &CachedSource{creds: creds, now: now}
}

// Token returns the held token, minting one when none is held.
func (s *CachedSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != "" {
		return s.current, nil
	}
	return s.mint(ctx, false)
}

// Refresh mints a successor to stale unless one is already held.
func (s *CachedSource) Refresh(ctx context.Context, stale string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != "" && s.current != stale {
		return s.current, nil
	}
	return s.mint(ctx, true)
}

func (s *CachedSource) mint(ctx context.Context, refreshing bool) (string, error) {
	now := s.now()
	if since := now.Sub(s.resting); since < remintEvery {
		return "", fmt.Errorf("minting a Linear token: the last attempt was %s ago", since)
	}
	token, err := s.creds.mint(ctx)
	if refreshing || err != nil {
		s.resting = now
	}
	if err != nil {
		return "", err
	}
	s.current = token
	return token, nil
}

// Close revokes the held token.
func (s *CachedSource) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		return nil
	}
	err := s.creds.revoke(ctx, s.current)
	s.current = ""
	return err
}

// RevokeAll closes src on a context that outlives ctx's cancellation, and logs
// a failure as a warning rather than returning it.
func RevokeAll(ctx context.Context, src interface{ Close(context.Context) error }, logger *slog.Logger) {
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
	defer cancel()
	if err := src.Close(revokeCtx); err != nil {
		logger.Warn("linearRevokeFailed", "err", err)
	}
}
