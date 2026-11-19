package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	pollClockDocID     = "poll"
	lastPollField      = "last_poll_at"
	lastScheduledField = "last_scheduled_poll_at"
	wakeRequestedField = "wake_requested_at"
)

// PollClock keeps the dispatch/poll document: when the last poll started, when
// the last scheduled one did, and when a waker last asked for one, which a
// dispatcher execution compares to tell a wake from a scheduled tick. Both times are Firestore's own, so a waker's
// clock and the dispatcher's never disagree on which came first.
type PollClock struct {
	client *firestore.Client
}

// NewPollClock returns a PollClock backed by client.
func NewPollClock(client *firestore.Client) *PollClock {
	return &PollClock{client: client}
}

func (p *PollClock) doc() *firestore.DocumentRef {
	return p.client.Collection(dispatchCollection).Doc(pollClockDocID)
}

// RequestWake records that a waker asked for a poll, before it starts one.
func (p *PollClock) RequestWake(ctx context.Context) error {
	return p.stamp(ctx, "recording a wake request", wakeRequestedField)
}

// MarkPolled records that a poll starts now, before its first read, so a wake
// asked for before it is one the poll covers. A scheduled poll is also the one
// the next tick measures the owner's interval from; a wake's poll is not.
func (p *PollClock) MarkPolled(ctx context.Context, scheduled bool) error {
	if scheduled {
		return p.stamp(ctx, "recording the poll's start", lastPollField, lastScheduledField)
	}
	return p.stamp(ctx, "recording the poll's start", lastPollField)
}

// stamp sets each field to Firestore's time, leaving every other field as it is.
func (p *PollClock) stamp(ctx context.Context, doing string, fields ...string) error {
	data := make(map[string]any, len(fields))
	for _, f := range fields {
		data[f] = firestore.ServerTimestamp
	}
	if _, err := p.doc().Set(ctx, data, firestore.MergeAll); err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	return nil
}

// Times returns the last poll's start, the last scheduled poll's and the last
// wake request, each zero while never recorded.
func (p *PollClock) Times(ctx context.Context) (dispatcher.PollTimes, error) {
	snap, err := p.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.PollTimes{}, nil
	}
	if err != nil {
		return dispatcher.PollTimes{}, fmt.Errorf("reading the poll times: %w", err)
	}
	data := snap.Data()
	last, _ := data[lastPollField].(time.Time)
	scheduled, _ := data[lastScheduledField].(time.Time)
	wake, _ := data[wakeRequestedField].(time.Time)
	return dispatcher.PollTimes{LastPoll: last, LastScheduled: scheduled, WakeRequested: wake}, nil
}
