package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// autonomiesOver returns Autonomies over the emulator with no document, and
// removes the document afterwards.
func autonomiesOver(t *testing.T) (*Autonomies, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(autonomyDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewAutonomies(client), client
}

func TestAutonomyIsZeroWhileNoneIsSet(t *testing.T) {
	a, _ := autonomiesOver(t)
	if level, err := a.Level(context.Background()); err != nil || level != 0 {
		t.Fatalf("level = %d, %v, want 0 with no document", level, err)
	}
}

func TestAutonomySetIsReadBackOnTheNextRead(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	if err := a.Set(ctx, 3, noticeAt); err != nil {
		t.Fatal(err)
	}
	if level, err := a.Level(ctx); err != nil || level != 3 {
		t.Fatalf("level = %d, %v, want 3", level, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data(); got[levelField] != int64(3) || !got[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want level 3 set at %v", got, noticeAt)
	}
}

func TestAutonomySetRefusesALevelTheOwnerMayNotSet(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	for level, want := range map[dispatcher.Autonomy]error{
		4: dispatcher.ErrAutonomyNotEarned, 5: dispatcher.ErrAutonomyNotEarned, 6: dispatcher.ErrUnknownAutonomy, -1: dispatcher.ErrUnknownAutonomy,
	} {
		if err := a.Set(ctx, level, noticeAt); !errors.Is(err, want) {
			t.Errorf("Set(%d) err = %v, want %v", level, err, want)
		}
	}
	if _, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Get(ctx); err == nil {
		t.Fatal("a refused Set wrote the document")
	}
}

func TestAutonomyRefusesAStoredValueNamingNoLevel(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	for name, doc := range map[string]map[string]any{
		"a level out of range": {levelField: int64(9)},
		"a level as a string":  {levelField: "4"},
		"no level field":       {setAtField: noticeAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Set(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if level, err := a.Level(ctx); !errors.Is(err, dispatcher.ErrUnknownAutonomy) || level != 0 {
				t.Fatalf("Level = %d, %v, want ErrUnknownAutonomy so the dispatcher counts it as 0", level, err)
			}
		})
	}
}

// claimedAutonomy reads the level run id's row records, and whether it records one.
func claimedAutonomy(t *testing.T, client *firestore.Client, id string) (any, bool) {
	t.Helper()
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v, ok := snap.Data()[autonomyField]
	return v, ok
}

func TestTryClaimRecordsTheLevelOnceAndALaterClaimKeepsIt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-autonomy"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 2)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, recorded := claimedAutonomy(t, client, run.RunID); recorded {
		t.Fatal("a queued run records a level before any claim")
	}
	at3 := openFacts
	at3.Autonomy = 3
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, at3, dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StagePlan}); err != nil || !ok {
		t.Fatalf("plan claim: ok %v, err %v", ok, err)
	}
	rec, err := NewRecords(client).GetRecord(ctx, run.RunID)
	if err != nil || rec.Autonomy != 3 {
		t.Fatalf("record autonomy = %d, %v, want 3 as pr-meta reads it", rec.Autonomy, err)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{{Path: stateField, Value: statePlanned}}); err != nil {
		t.Fatal(err)
	}
	at1 := openFacts
	at1.Autonomy = 1
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, at1, dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild}); err != nil || !ok {
		t.Fatalf("build claim: ok %v, err %v", ok, err)
	}
	if got, _ := claimedAutonomy(t, client, run.RunID); got != int64(3) {
		t.Fatalf("autonomy = %v after the build claim, want the plan claim's 3 kept", got)
	}
}

func TestRequeueClearsTheLevelSoTheNextAttemptRecordsItsOwn(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-autonomy-requeue"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	at2 := openFacts
	at2.Autonomy = 2
	res := dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StagePlan}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, at2, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Stop(ctx, run.RunID, runner.StopPlanStageFailed, "the plan stage failure", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Requeue(ctx, run.RunID, queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, recorded := claimedAutonomy(t, client, run.RunID); recorded {
		t.Fatalf("a requeued run still records level %v", got)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt.Add(3*time.Minute), generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("second claim: ok %v, err %v", ok, err)
	}
	if got, _ := claimedAutonomy(t, client, run.RunID); got != int64(0) {
		t.Fatalf("autonomy = %v, want the second attempt's 0", got)
	}
}
