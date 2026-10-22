package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

// IAP signs every request it lets through with an ES256 JWT on this header,
// issued by iapIssuer for the service's own audience
// (https://docs.cloud.google.com/iap/docs/signed-headers-howto).
const (
	iapAssertionHeader = "X-Goog-Iap-Jwt-Assertion"
	iapIssuer          = "https://cloud.google.com/iap"
	// es256SignatureSize is r and s, 32 bytes each.
	es256SignatureSize = 64
	// verifyTimeout bounds a verification, which may fetch IAP's public keys.
	verifyTimeout = 5 * time.Second
)

// TokenValidator checks a JWT's signature, audience and expiry and returns its
// payload; *idtoken.Validator is the production one, keyed by IAP's public keys.
type TokenValidator interface {
	Validate(ctx context.Context, token, audience string) (*idtoken.Payload, error)
}

var (
	errNoAssertion  = errors.New("the request carries no IAP assertion")
	errNotES256     = errors.New("the assertion is not a JWT with an ES256 signature")
	errWrongIssuer  = errors.New("the assertion was not issued by IAP")
	errNoEmailClaim = errors.New("the assertion carries no email")
)

type emailKey struct{}

// requestEmail is the address the request is signed in as, or nil for one
// that reached the mux without passing gate.
func requestEmail(r *http.Request) *string {
	email, ok := r.Context().Value(emailKey{}).(string)
	if !ok {
		return nil
	}
	return &email
}

// requireIAP refuses, with 401, every request without a valid IAP assertion for
// audience, and passes the rest on with the assertion's email in their context.
// The health check is exempt: the platform calls it directly, and it serves no
// page or data.
func requireIAP(v TokenValidator, audience string, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		email, err := verifyAssertion(r.Context(), v, audience, r.Header.Get(iapAssertionHeader))
		if err != nil {
			logger.Warn("iapAssertionRefused", "err", err.Error(), "path", r.URL.Path)
			http.Error(w, "this service is reached through IAP", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, withEmail(r, email))
	})
}

// signedInAs passes every request on signed in as email, for local
// development, where no IAP fronts the process.
func signedInAs(email string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, withEmail(r, email))
	})
}

func withEmail(r *http.Request, email string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), emailKey{}, email))
}

// verifyAssertion checks the assertion carries an ES256 signature that v
// validates for audience and was issued by IAP, and returns its email claim.
// Google's RS256 ID tokens, which v also accepts, fail the issuer check.
func verifyAssertion(ctx context.Context, v TokenValidator, audience, token string) (string, error) {
	if token == "" {
		return "", errNoAssertion
	}
	if !hasES256Signature(token) {
		return "", errNotES256
	}
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	payload, err := v.Validate(ctx, token, audience)
	if err != nil {
		return "", err
	}
	if payload.Issuer != iapIssuer {
		return "", errWrongIssuer
	}
	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return "", errNoEmailClaim
	}
	return email, nil
}

// hasES256Signature reports whether the token is three segments whose
// signature is the 64 bytes an ES256 signature is. The validator slices an
// ES256 signature without checking its length, so a shorter one would panic.
func hasES256Signature(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil && len(sig) == es256SignatureSize
}
