package webhook

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestReloadKeepsTheHeldValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		err   error
	}{
		{"read fails", "", errors.New("unavailable")},
		{"read is empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSecret("test", "held", func(context.Context) (string, error) { return tc.value, tc.err },
				func() time.Time { return receivedAt }, slog.New(slog.DiscardHandler))
			if got := s.Reload(context.Background()); got != "held" || s.Value() != "held" {
				t.Fatalf("reload = %q, value = %q, want held", got, s.Value())
			}
		})
	}
}
