package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// pollIntervalOver returns PollInterval over the emulator with no document, and
// removes the document afterwards.
func pollIntervalOver(t *testing.T) (*PollInterval, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(settingsCollection).Doc(pollIntervalDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewPollInterval(client), client
}

func TestPollIntervalDefaultsToFifteenWhileNoneIsSet(t *testing.T) {
	p, _ := pollIntervalOver(t)
	if n, err := p.Value(context.Background()); err != nil || n != 15 {
		t.Fatalf("value = %d, %v, want 15 with no document", n, err)
	}
}

func TestPollIntervalSetIsReadBackAndRecordsWhen(t *testing.T) {
	p, client := pollIntervalOver(t)
	ctx := context.Background()
	for _, n := range []int{dispatcher.DefaultPollMinutes, dispatcher.FastPollMinutes} {
		if err := p.Set(ctx, n, noticeAt); err != nil {
			t.Fatal(err)
		}
		if got, err := p.Value(ctx); err != nil || got != n {
			t.Fatalf("value = %d, %v, want %d", got, err, n)
		}
	}
	snap, err := client.Collection(settingsCollection).Doc(pollIntervalDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := snap.Data()
	if stored[pollIntervalField] != int64(10) || !stored[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want 10 set at %v", stored, noticeAt)
	}
}

func TestPollIntervalRefusesAnyOtherValueAndWritesNothing(t *testing.T) {
	p, client := pollIntervalOver(t)
	ctx := context.Background()
	for _, n := range []int{0, 9, 12, 14, 16} {
		if err := p.Set(ctx, n, noticeAt); err == nil {
			t.Fatalf("Set(%d) succeeded, want it refused", n)
		}
	}
	if _, err := client.Collection(settingsCollection).Doc(pollIntervalDocID).Get(ctx); err == nil {
		t.Fatal("a refused Set wrote the document")
	}
}

func TestPollIntervalRefusesAStoredValueItCannotUse(t *testing.T) {
	p, client := pollIntervalOver(t)
	ctx := context.Background()
	for name, doc := range map[string]map[string]any{
		"a value between the two intervals": {pollIntervalField: 12},
		"a value past the range":            {pollIntervalField: 60},
		"a value that is not a number":      {pollIntervalField: "10"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(settingsCollection).Doc(pollIntervalDocID).Set(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if n, err := p.Value(ctx); err == nil {
				t.Fatalf("Value = %d, want an error so the dispatcher falls back to 15", n)
			}
		})
	}
}
