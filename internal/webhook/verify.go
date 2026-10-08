// Package webhook receives Linear's agent-session webhook and wakes the
// dispatcher, so a delegated ticket is admitted in seconds rather than at the
// next scheduled poll. It sizes and selects nothing: the poll it wakes does.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const (
	signatureHeader = "Linear-Signature"
	maxSkew         = 60 * time.Second
)

// verify reports whether signature is the hex HMAC-SHA256 of body under secret.
// An empty secret verifies nothing, since anyone can sign with an empty key.
func verify(secret string, body []byte, signature string) bool {
	if secret == "" {
		return false
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}

// fresh reports whether a webhookTimestamp in milliseconds lies within maxSkew
// of now, either side, so a captured delivery cannot be replayed later.
func fresh(ms int64, now time.Time) bool {
	skew := now.Sub(time.UnixMilli(ms))
	return skew <= maxSkew && skew >= -maxSkew
}
