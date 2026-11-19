package store

import (
	"context"
	"testing"
	"time"
)

// pollClockOver returns PollClock over the emulator with no document, and
// removes the document afterwards.
func pollClockOver(t *testing.T) *PollClock {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(pollClockDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewPollClock(client)
}

func TestPollClockReadsZeroTimesWhileNothingIsRecorded(t *testing.T) {
	times, err := pollClockOver(t).Times(context.Background())
	if err != nil || !times.LastPoll.IsZero() || !times.WakeRequested.IsZero() {
		t.Fatalf("times = %+v, %v, want both zero", times, err)
	}
}

// TestPollClockOrdersAWakeAndThePollThatFollows covers the wake an execution
// must see: a wake request recorded after a scheduled poll reads as later than
// it, and the wake's own poll then reads as later than the wake while leaving
// the scheduled poll's time alone. The writes are spaced apart because the
// emulator stamps to the millisecond.
func TestPollClockOrdersAWakeAndThePollThatFollows(t *testing.T) {
	p := pollClockOver(t)
	ctx := context.Background()
	if err := p.MarkPolled(ctx, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := p.RequestWake(ctx); err != nil {
		t.Fatal(err)
	}
	woken, err := p.Times(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if woken.LastPoll.IsZero() || !woken.LastScheduled.Equal(woken.LastPoll) || !woken.WakeRequested.After(woken.LastPoll) {
		t.Fatalf("times = %+v, want one scheduled poll and the wake after it", woken)
	}
	time.Sleep(5 * time.Millisecond)
	if err := p.MarkPolled(ctx, false); err != nil {
		t.Fatal(err)
	}
	polled, err := p.Times(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !polled.WakeRequested.Equal(woken.WakeRequested) || !polled.LastPoll.After(polled.WakeRequested) ||
		!polled.LastScheduled.Equal(woken.LastScheduled) {
		t.Fatalf("times = %+v, want the wake kept, the poll after it and the scheduled poll unmoved", polled)
	}
}
