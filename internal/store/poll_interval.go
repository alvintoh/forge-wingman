package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	pollIntervalDocID = "poll-interval"
	pollIntervalField = "poll_minutes"
)

// PollInterval keeps the owner's poll interval in the owner database: how many
// minutes a scheduled tick waits after the last poll, which the surface sets and
// every tick reads (adr/0019).
type PollInterval struct {
	setting intSetting
}

// NewPollInterval returns a PollInterval backed by client, which must be one
// opened on OwnerDatabase.
func NewPollInterval(client *firestore.Client) *PollInterval {
	return &PollInterval{setting: intSetting{
		client: client, docID: pollIntervalDocID, field: pollIntervalField, name: "poll interval",
		min: dispatcher.FastPollMinutes, max: dispatcher.DefaultPollMinutes, def: dispatcher.DefaultPollMinutes,
	}}
}

// Value returns the saved interval in minutes, DefaultPollMinutes while none is
// saved. A stored value that is not an interval the owner may save is an error,
// so the dispatcher falls back to the default.
func (p *PollInterval) Value(ctx context.Context) (int, error) {
	n, err := p.setting.value(ctx)
	if err != nil {
		return 0, err
	}
	if !dispatcher.ValidPollMinutes(n) {
		return 0, notAPollInterval("reading", n)
	}
	return n, nil
}

// Set saves the interval in minutes, effective from the next tick.
func (p *PollInterval) Set(ctx context.Context, n int, at time.Time) error {
	if !dispatcher.ValidPollMinutes(n) {
		return notAPollInterval("setting", n)
	}
	return p.setting.set(ctx, n, at)
}

// notAPollInterval is the error for n, which is not an interval the owner may save.
func notAPollInterval(doing string, n int) error {
	return fmt.Errorf("%s the poll interval: %d is not %d or %d minutes",
		doing, n, dispatcher.FastPollMinutes, dispatcher.DefaultPollMinutes)
}
