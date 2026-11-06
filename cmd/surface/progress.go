package main

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/alvintoh/forge-wingman/internal/store"
)

const (
	progressReads     = 4
	progressTimeout   = 3 * time.Second
	progressFreshFor  = 60 * time.Second
	progressFailedFor = 10 * time.Second
)

// progressSource reads one running build's newest progress, nil when it has
// written none.
type progressSource interface {
	Progress(ctx context.Context, run store.Running, now time.Time) (*store.Progress, error)
}

// progressKey names the progress folder of one reservation.
type progressKey struct {
	RunID   string
	Attempt int
	Stage   string
}

func progressKeyOf(run store.Running) progressKey {
	return progressKey{RunID: run.RunID, Attempt: run.Attempt, Stage: run.Stage}
}

type progressEntry struct {
	progress *store.Progress
	expires  time.Time
}

// cachedProgress is a ProgressReader over a progressSource: it shares one read
// per run among concurrent requests and keeps each answer for a minute, or a
// failure for ten seconds.
type cachedProgress struct {
	source  progressSource
	logger  *slog.Logger
	now     func() time.Time
	timeout time.Duration

	flight  singleflight.Group
	mu      sync.Mutex
	entries map[progressKey]progressEntry
}

func newCachedProgress(source progressSource, logger *slog.Logger) *cachedProgress {
	return &cachedProgress{source: source, logger: logger, now: time.Now, timeout: progressTimeout,
		entries: map[progressKey]progressEntry{}}
}

// Progress reads every run's progress, at most four at a time and all within
// one deadline; a run whose read failed or did not finish in time has none.
// The runs the deadline cut short are logged once, with their count.
func (c *cachedProgress) Progress(ctx context.Context, runs []store.Running) map[progressKey]*store.Progress {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var mu sync.Mutex
	out := make(map[progressKey]*store.Progress, len(runs))
	var cut atomic.Int32
	var g errgroup.Group
	g.SetLimit(progressReads)
	for _, run := range runs {
		g.Go(func() error {
			p, ok := c.read(ctx, run)
			if !ok {
				cut.Add(1)
			}
			if p != nil {
				mu.Lock()
				out[progressKeyOf(run)] = p
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	if n := cut.Load(); n > 0 {
		c.logger.Warn("progressNotRead", "reason", "deadline", "runs", n)
	}
	return out
}

// read is run's progress, false when the deadline passed before it was read.
func (c *cachedProgress) read(ctx context.Context, run store.Running) (*store.Progress, bool) {
	key := progressKeyOf(run)
	if p, ok := c.cached(key); ok {
		return p, true
	}
	if ctx.Err() != nil {
		return nil, false
	}
	flightKey := key.RunID + "/" + strconv.Itoa(key.Attempt) + "/" + key.Stage
	ch := c.flight.DoChan(flightKey, func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		defer cancel()
		p, err := c.source.Progress(rctx, run, c.now())
		ttl := progressFreshFor
		if err != nil {
			c.logger.Warn("progressNotRead", "reason", "read-failed", "run", run.RunID, "attempt", run.Attempt, "stage", run.Stage, "err", err.Error())
			p, ttl = nil, progressFailedFor
		}
		c.store(key, p, ttl)
		return p, nil
	})
	select {
	case res := <-ch:
		p, _ := res.Val.(*store.Progress)
		return p, true
	case <-ctx.Done():
		return nil, false
	}
}

func (c *cachedProgress) cached(key progressKey) (*store.Progress, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expires) {
		return nil, false
	}
	return e.progress, true
}

// store keeps p for ttl and drops every entry that has expired, so runs that
// have settled leave the cache.
func (c *cachedProgress) store(key progressKey, p *store.Progress, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = progressEntry{progress: p, expires: now.Add(ttl)}
}
