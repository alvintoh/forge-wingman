package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
)

// peakModesOver returns PeakModes over the emulator with no document, and
// removes the document afterwards.
func peakModesOver(t *testing.T) (*PeakModes, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(peakModeDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewPeakModes(client), client
}

func TestPeakModeDefaultsToOffPeakOnlyWhileNoneIsSet(t *testing.T) {
	p, _ := peakModesOver(t)
	mode, err := p.Mode(context.Background())
	if err != nil || mode != "off-peak-only" {
		t.Fatalf("mode = %q, %v, want off-peak-only with no document", mode, err)
	}
}

func TestPeakModeSetIsReadBackOnTheNextRead(t *testing.T) {
	p, client := peakModesOver(t)
	ctx := context.Background()
	if err := p.Set(ctx, dispatcher.PeakAlways, noticeAt); err != nil {
		t.Fatal(err)
	}
	if mode, err := p.Mode(ctx); err != nil || mode != "always" {
		t.Fatalf("mode = %q, %v, want always", mode, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(peakModeDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data(); got[modeField] != "always" || !got[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want mode always set at %v", got, noticeAt)
	}
	if err := p.Set(ctx, dispatcher.PeakOffPeakOnly, noticeAt); err != nil {
		t.Fatal(err)
	}
	if mode, err := p.Mode(ctx); err != nil || mode != "off-peak-only" {
		t.Fatalf("mode = %q, %v, want off-peak-only after the switch back", mode, err)
	}
}

func TestPeakModeRefusesAValueNamingNoMode(t *testing.T) {
	p, client := peakModesOver(t)
	ctx := context.Background()
	if err := p.Set(ctx, "sometimes", noticeAt); !errors.Is(err, dispatcher.ErrUnknownPeakMode) {
		t.Fatalf("Set err = %v, want ErrUnknownPeakMode", err)
	}
	if _, err := client.Collection(dispatchCollection).Doc(peakModeDocID).Get(ctx); err == nil {
		t.Fatal("a refused Set wrote the document")
	}
	for name, doc := range map[string]map[string]any{
		"an unknown mode": {modeField: "sometimes"},
		"no mode field":   {setAtField: noticeAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(dispatchCollection).Doc(peakModeDocID).Set(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if mode, err := p.Mode(ctx); !errors.Is(err, dispatcher.ErrUnknownPeakMode) || mode != "" {
				t.Fatalf("Mode = %q, %v, want ErrUnknownPeakMode so the dispatcher holds", mode, err)
			}
		})
	}
}

func TestTryClaimHoldsAPeakHeldRunAndRecordsWhy(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-peak"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	held := openFacts
	held.PeakHeld = true
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, held, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "peak-hours" {
		t.Fatalf("ok = %v, binding = %q, want held on peak-hours", ok, binding)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateQueued || snap.Data()[waitingOnField] != "peak-hours" {
		t.Fatalf("stored state %v, waiting on %v, want queued, waiting on peak-hours", snap.Data()[stateField], snap.Data()[waitingOnField])
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim once the window passed: ok %v, err %v", ok, err)
	}
	snap, err = client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, waiting := snap.Data()[waitingOnField]; waiting {
		t.Fatalf("a claimed run still records %v as what it waits on", snap.Data()[waitingOnField])
	}
}
