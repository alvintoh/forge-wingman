package collect

import (
	"testing"
	"time"

	"acctsvc/internal/store"
)

func TestDefaultConfigPacesPerSecond(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Limiter.Interval != time.Second {
		t.Errorf("Interval = %v, want 1s", cfg.Limiter.Interval)
	}
}

func TestRunResolvesEveryInboundRecord(t *testing.T) {
	st := store.New()
	// an explicit burst: DefaultConfig leaves it unset, which Run rejects
	cfg := DefaultConfig()
	cfg.Limiter.Burst = 2
	c := NewCollector(cfg, st)
	inbound := []string{"ada@example.com", "grace@example.com", "alan@example.com"}
	got, err := c.Run(inbound)
	if err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if len(got) != len(inbound) {
		t.Errorf("resolved %d records, want %d", len(got), len(inbound))
	}
}
