package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	// heldOnField maps each Sydney date a poll held the run on to the reasons it
	// was held for that day, and heldAtField is when the latest hold was
	// recorded, which a read of the trailing window queries by.
	heldOnField = "held_on"
	heldAtField = "held_at"
)

// RecordHold adds reason to the run's holds for day, in one write that also
// stamps when. A reason the day already holds is not added again, so a second
// recording of the same run, reason and day counts once.
func (q *Queue) RecordHold(ctx context.Context, runID, reason, day string, at time.Time) error {
	_, err := q.client.Collection(runsCollection).Doc(runID).Update(ctx, []firestore.Update{
		{FieldPath: firestore.FieldPath{heldOnField, day}, Value: firestore.ArrayUnion(reason)},
		{Path: heldAtField, Value: at},
	})
	if err != nil {
		return fmt.Errorf("recording run %s held for %s: %w", runID, reason, err)
	}
	return nil
}

// heldOn is the holds a run row records, by day.
func heldOn(row map[string]any) map[string][]string {
	days, _ := row[heldOnField].(map[string]any)
	if len(days) == 0 {
		return nil
	}
	out := make(map[string][]string, len(days))
	for day, reasons := range days {
		list, _ := reasons.([]any)
		for _, r := range list {
			if s, ok := r.(string); ok {
				out[day] = append(out[day], s)
			}
		}
	}
	return out
}

// ConcurrencyStats is what the runs of the trailing window say about running
// several at once: the most that were ever in flight together, the N admission
// holds now, and how many holds each reason counts.
type ConcurrencyStats struct {
	Peak int
	N    int
	Held map[string]int
}

// ConcurrencyStats reads the trailing window ending at now, which
// dispatcher.HoldWindowStart opens. The peak is the largest number of runs in
// flight any claim in the window counted, itself included: the number in flight
// only rises when a run is claimed, so the most a claim saw is the most there
// ever were. Each run, reason and day held in the window counts once.
func (q *Queue) ConcurrencyStats(ctx context.Context, now time.Time) (ConcurrencyStats, error) {
	ledger, err := q.ledgerOutsideTx(ctx)
	if err != nil {
		return ConcurrencyStats{}, err
	}
	since := dispatcher.HoldWindowStart(now)
	stats := ConcurrencyStats{N: ledger.concurrency().N, Held: map[string]int{}}
	err = eachRow(q.client.Collection(runsCollection).
		Where(claimedAtField, ">=", since).Select(claimConcurrencyField).Documents(ctx), func(_ string, row map[string]any) {
		if n, _ := row[claimConcurrencyField].(int64); int(n) > stats.Peak {
			stats.Peak = int(n)
		}
	})
	if err != nil {
		return ConcurrencyStats{}, fmt.Errorf("reading the runs claimed since %s: %w", since.Format(time.DateOnly), err)
	}
	first := dispatcher.HoldDay(since)
	err = eachRow(q.client.Collection(runsCollection).
		Where(heldAtField, ">=", since).Select(heldOnField).Documents(ctx), func(_ string, row map[string]any) {
		for day, reasons := range heldOn(row) {
			if day < first {
				continue
			}
			for _, reason := range reasons {
				stats.Held[reason]++
			}
		}
	})
	if err != nil {
		return ConcurrencyStats{}, fmt.Errorf("reading the runs held since %s: %w", first, err)
	}
	return stats, nil
}
