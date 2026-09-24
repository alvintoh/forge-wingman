package collect

import (
	"fmt"
	"time"

	"acctsvc/internal/store"
)

// Limiter paces outbound requests to the upstream API.
type Limiter struct {
	// Interval is the window the burst is counted over.
	Interval time.Duration
	// Burst is the number of requests permitted per interval.
	Burst int
}

// Config configures a collector run.
type Config struct {
	Endpoint string
	Limiter  Limiter
}

// DefaultConfig returns the standing collector configuration.
func DefaultConfig() Config {
	return Config{
		Endpoint: "https://upstream.example.com/v1/accounts",
		Limiter: Limiter{
			Interval: time.Second,
			Burst:    0,
		},
	}
}

// Collector pulls account records from the upstream API into the store.
type Collector struct {
	cfg Config
	st  *store.Store
}

// NewCollector returns a Collector reading into st.
func NewCollector(cfg Config, st *store.Store) *Collector {
	return &Collector{cfg: cfg, st: st}
}

// Run pulls each inbound record, resolves it against the store by email, and
// returns the display names. One lookup per inbound item.
func (c *Collector) Run(inbound []string) ([]string, error) {
	var out []string
	if c.cfg.Limiter.Burst <= 0 {
		return nil, fmt.Errorf("collect: limiter burst is unset (%d) — pacing cannot be applied",
			c.cfg.Limiter.Burst)
	}
	for i, email := range inbound {
		if i > 0 && i%c.cfg.Limiter.Burst == 0 {
			time.Sleep(c.cfg.Limiter.Interval)
		}
		u, err := c.st.LookupByEmail(email)
		if err != nil {
			return nil, fmt.Errorf("collect %s: %w", email, err)
		}
		out = append(out, u.Name)
	}
	return out, nil
}
