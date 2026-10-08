package webhook

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const reloadEvery = 30 * time.Second

// Secret holds a value read from the secret store and re-reads it on demand,
// at most once per reloadEvery, so a flood of requests cannot hammer the store.
type Secret struct {
	name   string
	read   func(context.Context) (string, error)
	now    func() time.Time
	logger *slog.Logger

	mu       sync.Mutex
	value    string
	lastRead time.Time
}

// NewSecret starts from value; read fetches the named secret's current value.
func NewSecret(name, value string, read func(context.Context) (string, error), now func() time.Time, logger *slog.Logger) *Secret {
	return &Secret{name: name, value: value, read: read, now: now, logger: logger}
}

// Value is the value held now.
func (s *Secret) Value() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// Reload re-reads the value, keeping a non-empty one it reads, and returns the
// value held after; inside reloadEvery of the last read it reads nothing.
func (s *Secret) Reload(ctx context.Context) string {
	s.mu.Lock()
	now := s.now()
	if !s.lastRead.IsZero() && now.Sub(s.lastRead) < reloadEvery {
		defer s.mu.Unlock()
		return s.value
	}
	s.lastRead = now
	s.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	value, err := s.read(callCtx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.logger.Warn("webhookSecretReloadFailed", "secret", s.name, "err", err)
		return s.value
	}
	if value != "" {
		s.value = value
	}
	return s.value
}
