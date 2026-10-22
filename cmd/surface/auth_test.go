package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/api/idtoken"
)

const (
	testAudience = "/projects/123/locations/us-central1/services/forge-wingman-surface"
	testKeyID    = "test-key"
	rsaKeyID     = "rsa-key"
)

// keysTransport answers every request with the one JWK set it holds, standing
// in for IAP's public key endpoint.
type keysTransport struct{ body []byte }

func (k keysTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(k.body)),
	}, nil
}

func newTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// rsaKey signs the RS256 token the validator would accept as a Google ID token;
// testValidator lists its public half beside the ES256 key.
var rsaKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

// testValidator is the real idtoken validator, reading its keys from a JWK set
// that holds key's public half and rsaKey's.
func testValidator(t *testing.T, key *ecdsa.PrivateKey) *idtoken.Validator {
	t.Helper()
	pad := func(b []byte) string {
		out := make([]byte, 32)
		copy(out[32-len(b):], b)
		return base64.RawURLEncoding.EncodeToString(out)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"alg": "ES256", "crv": "P-256", "kid": testKeyID, "kty": "EC", "use": "sig",
		"x": pad(key.X.Bytes()), "y": pad(key.Y.Bytes()),
	}, {
		"alg": "RS256", "kid": rsaKeyID, "kty": "RSA", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(rsaKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := idtoken.NewValidator(context.Background(),
		idtoken.WithHTTPClient(&http.Client{Transport: keysTransport{body: jwks}}))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// signRS256 mints a JWT over claims signed RS256 by rsaKey, as Google signs
// its own ID tokens.
func signRS256(t *testing.T, claims map[string]any) string {
	t.Helper()
	signed := segment(t, map[string]string{"alg": "RS256", "kid": rsaKeyID, "typ": "JWT"}) + "." + segment(t, claims)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func segment(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// sign mints a JWT with the given algorithm over claims, signed ES256 by key.
func sign(t *testing.T, key *ecdsa.PrivateKey, alg string, claims map[string]any) string {
	t.Helper()
	signed := segment(t, map[string]string{"alg": alg, "kid": testKeyID, "typ": "JWT"}) + "." + segment(t, claims)
	digest := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// shortSignature is a valid token with its signature cut to 16 bytes, shorter
// than the half the validator slices off without checking.
func shortSignature(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	tok := sign(t, key, "ES256", iapClaims())
	i := strings.LastIndex(tok, ".")
	sig, err := base64.RawURLEncoding.DecodeString(tok[i+1:])
	if err != nil {
		t.Fatal(err)
	}
	return tok[:i+1] + base64.RawURLEncoding.EncodeToString(sig[:16])
}

func iapClaims() map[string]any {
	return map[string]any{
		"iss":   "https://cloud.google.com/iap",
		"aud":   testAudience,
		"exp":   time.Now().Add(time.Minute).Unix(),
		"iat":   time.Now().Unix(),
		"email": "owner@example.com",
	}
}

func gatedMux(t *testing.T, key *ecdsa.PrivateKey) http.Handler {
	t.Helper()
	spa := fstest.MapFS{"index.html": {Data: []byte("<html>shell</html>")}}
	return requireIAP(testValidator(t, key), testAudience, discardLogger(), newMux(Deps{SPA: spa, Logger: discardLogger()}))
}

func serve(h http.Handler, path, assertion string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if assertion != "" {
		req.Header.Set("x-goog-iap-jwt-assertion", assertion)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIAPAssertionServesItsEmail(t *testing.T) {
	key := newTestKey(t)
	rec := serve(gatedMux(t, key), "/api/me", sign(t, key, "ES256", iapClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), "{\"email\":\"owner@example.com\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestIAPRefusesARequestWithoutAValidAssertion(t *testing.T) {
	key := newTestKey(t)
	with := func(k string, v any) map[string]any {
		c := iapClaims()
		c[k] = v
		return c
	}
	noEmail := iapClaims()
	delete(noEmail, "email")

	tests := []struct {
		name      string
		assertion string
	}{
		{"missing", ""},
		{"not a JWT", "not-a-jwt"},
		{"wrong audience", sign(t, key, "ES256", with("aud", "/projects/123/locations/us-central1/services/other"))},
		{"wrong issuer", sign(t, key, "ES256", with("iss", "https://accounts.google.com"))},
		{"expired", sign(t, key, "ES256", with("exp", time.Now().Add(-time.Minute).Unix()))},
		{"signed by another key", sign(t, newTestKey(t), "ES256", iapClaims())},
		{"a Google RS256 ID token", signRS256(t, iapClaims())},
		{"a short signature", shortSignature(t, key)},
		{"no email", sign(t, key, "ES256", noEmail)},
	}
	h := gatedMux(t, key)
	for _, tt := range tests {
		for _, path := range []string{"/api/me", "/"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				rec := serve(h, path, tt.assertion)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", rec.Code)
				}
				if body := rec.Body.String(); strings.Contains(body, "shell") || strings.Contains(body, "email") {
					t.Fatalf("a refused request was served %q", body)
				}
			})
		}
	}
}

func TestIAPLogsAMissingAssertionAsMissing(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := requireIAP(testValidator(t, newTestKey(t)), testAudience, logger, http.NotFoundHandler())
	serve(h, "/api/me", "")
	if !strings.Contains(logs.String(), "the request carries no IAP assertion") {
		t.Fatalf("log = %q, want the missing assertion named", logs.String())
	}
}

func TestIAPLetsTheHealthCheckThroughWithoutAnAssertion(t *testing.T) {
	if rec := serve(gatedMux(t, newTestKey(t)), "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGateInLocalDevelopmentServesNoAccount(t *testing.T) {
	t.Setenv("WINGMAN_LOCAL_DEV", "1")
	t.Setenv("WINGMAN_IAP_AUDIENCE", "")
	h, err := gate(context.Background(), discardLogger(), newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()}))
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, "/api/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "{\"email\":null}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestGateOutsideLocalDevelopmentNeedsAnAudience(t *testing.T) {
	t.Setenv("WINGMAN_LOCAL_DEV", "")
	t.Setenv("WINGMAN_IAP_AUDIENCE", "")
	if _, err := gate(context.Background(), discardLogger(), http.NotFoundHandler()); err == nil {
		t.Fatal("gate started with no audience outside local development")
	}
}

func TestGateOutsideLocalDevelopmentRefusesARequestWithoutAnAssertion(t *testing.T) {
	t.Setenv("WINGMAN_LOCAL_DEV", "")
	t.Setenv("WINGMAN_IAP_AUDIENCE", testAudience)
	h, err := gate(context.Background(), discardLogger(), newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()}))
	if err != nil {
		t.Fatal(err)
	}
	if rec := serve(h, "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
