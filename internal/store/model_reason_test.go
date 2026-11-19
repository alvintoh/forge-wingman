package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	prov "github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The reasons a claim hands the store, as the dispatcher words them.
const (
	chosenM    = "cheapest eligible for size M at 07:00 UTC"
	noEligible = "no eligible model for size M"
)

// buildModelReason is why a run builds on its model, as its row holds it.
type buildModelReason struct {
	Reason       string
	RatesVersion string
}

// reasonOf reads id's build model reason and rates version off its row.
func reasonOf(t *testing.T, client *firestore.Client, id string) buildModelReason {
	t.Helper()
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	return buildModelReason{Reason: str(data, modelReasonField), RatesVersion: str(data, ratesVersionField)}
}

// claimReason enqueues run, claims it with res and returns why it then builds
// on its model.
func claimReason(t *testing.T, q *Queue, client *firestore.Client, run dispatcher.Queued, res dispatcher.Reservation) buildModelReason {
	t.Helper()
	ctx := context.Background()
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim of %s = %v, %v", run.RunID, ok, err)
	}
	return reasonOf(t, client, run.RunID)
}

func TestTryClaimRecordsWhyTheRunBuildsOnItsModel(t *testing.T) {
	const chosen, named = "opencode-go/claude-haiku-5-5", "opencode-go/kimi-k3"
	version := prov.RatesVersion()
	for name, tt := range map[string]struct {
		label string
		res   dispatcher.Reservation
		want  buildModelReason
	}{
		"a chosen model, with the rates it was ranked by": {
			res:  dispatcher.Reservation{ChosenModel: chosen, ModelReason: chosenM, RatesVersion: version},
			want: buildModelReason{Reason: chosenM, RatesVersion: version}},
		"the ticket's label, whatever the claim offers": {label: named,
			res:  dispatcher.Reservation{ChosenModel: chosen, ModelReason: chosenM, RatesVersion: version},
			want: buildModelReason{Reason: dispatcher.ReasonNamedByTicket}},
		"the default, with its own size": {
			res:  dispatcher.Reservation{ModelReason: noEligible},
			want: buildModelReason{Reason: noEligible}},
		"a claim the choice does not apply to": {},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			run := queuedRun(fresh("reason"), 1)
			run.Models = runner.ModelLabels{Build: tt.label}
			forget(t, client, run.RunID)
			resetLedger(t, client, 4)
			if got := claimReason(t, q, client, run, tt.res); got != tt.want {
				t.Fatalf("reason = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTryClaimKeepsTheReasonAnEarlierClaimRecorded(t *testing.T) {
	version := prov.RatesVersion()
	for name, first := range map[string]dispatcher.Reservation{
		"a chosen model's":         {ChosenModel: "opencode-go/claude-haiku-5-5", ModelReason: chosenM, RatesVersion: version},
		"the plan stage's default": {ModelReason: noEligible},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			ctx := context.Background()
			run := queuedRun(fresh("reason-kept"), 1)
			forget(t, client, run.RunID)
			resetLedger(t, client, 4)
			want := claimReason(t, q, client, run, first)
			if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{{Path: stateField, Value: statePlanned}}); err != nil {
				t.Fatal(err)
			}
			// The build stage, which the choice does not apply to.
			if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{Stage: dispatcher.StageBuild}); err != nil || !ok {
				t.Fatalf("second claim = %v, %v", ok, err)
			}
			if got := reasonOf(t, client, run.RunID); got != want {
				t.Fatalf("reason = %+v after the build claim, want the first claim's %+v", got, want)
			}
		})
	}
}

func TestTheLongestReasonAClaimCanWriteFitsTheBound(t *testing.T) {
	q, client := queue(t)
	limit := dispatcher.MaxModelReasonBytes
	// Two-byte characters filling the bound, and three-byte ones past it, so a
	// cut at the bound lands inside one.
	exact := strings.Repeat("é", limit/2)
	over := strings.Repeat("€", limit)
	for _, tt := range []struct{ reason, want string }{
		{exact, exact},
		{over, strings.Repeat("€", limit/3)},
	} {
		run := queuedRun(fresh("reason-long"), 1)
		forget(t, client, run.RunID)
		resetLedger(t, client, 4)
		got := claimReason(t, q, client, run, dispatcher.Reservation{ModelReason: tt.reason})
		if got.Reason != tt.want || len(got.Reason) > limit {
			t.Fatalf("reason read back as %q (%d bytes), want %q", got.Reason, len(got.Reason), tt.want)
		}
	}
}

// reasonRetry is chosenRetry with the reason and rates version its claim recorded.
func reasonRetry(t *testing.T, q *Queue, client *firestore.Client, prefix, model string) string {
	t.Helper()
	id := fresh(prefix)
	seedRow(t, client, id, retryRow(map[string]any{
		modelLabelsField: map[string]any{"build": model}, chosenModelField: model,
		modelReasonField: chosenM, ratesVersionField: prov.RatesVersion(),
	}))
	if _, err := q.RetryWithNote(context.Background(), id, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = archiveRef(client.Collection(runsCollection).Doc(id), 2).Delete(context.Background()) })
	return id
}

func TestARetryKeepsReplacesOrClearsTheReason(t *testing.T) {
	const chosen, named = "opencode-go/claude-haiku-5-5", "opencode-go/kimi-k3"
	q, client := queue(t)
	ctx := context.Background()
	kept := buildModelReason{Reason: chosenM, RatesVersion: prov.RatesVersion()}

	id := reasonRetry(t, q, client, "reason-retry", chosen)
	if _, changed, err := q.RefreshRetriedModels(ctx, id, runner.ModelLabels{}); err != nil || changed {
		t.Fatalf("changed = %v (err %v), want the chosen model and its reason left as they were", changed, err)
	}
	if got := reasonOf(t, client, id); got != kept {
		t.Fatalf("reason = %+v after a retry keeping the model, want %+v", got, kept)
	}
	if _, _, err := q.RefreshRetriedModels(ctx, id, runner.ModelLabels{Build: named}); err != nil {
		t.Fatal(err)
	}
	if got := reasonOf(t, client, id); got != (buildModelReason{Reason: dispatcher.ReasonNamedByTicket}) {
		t.Fatalf("reason = %+v after a label, want the ticket's with no rates", got)
	}
	if _, _, err := q.RefreshRetriedModels(ctx, id, runner.ModelLabels{}); err != nil {
		t.Fatal(err)
	}
	if got := reasonOf(t, client, id); got != (buildModelReason{}) {
		t.Fatalf("reason = %+v once the label is removed, want none until the claim gives one", got)
	}

	cleared := reasonRetry(t, q, client, "reason-clear", chosen)
	if ok, err := q.ClearChosenModel(ctx, cleared); err != nil || !ok {
		t.Fatalf("cleared = %v (err %v)", ok, err)
	}
	if got := reasonOf(t, client, cleared); got != (buildModelReason{}) {
		t.Fatalf("reason = %+v after the choice was cleared, want none until the claim chooses again", got)
	}
	// The next claim records the new choice's reason.
	resetLedger(t, client, 4)
	if ok, _, err := q.TryClaim(ctx, cleared, queueAt, generousBudget, openFacts, dispatcher.Reservation{ModelReason: noEligible}); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if got := reasonOf(t, client, cleared); got != (buildModelReason{Reason: noEligible}) {
		t.Fatalf("reason = %+v after the claim, want its new %q", got, noEligible)
	}
}

func TestTryClaimGivesAModelChosenBeforeReasonsWereRecordedItsReason(t *testing.T) {
	const chosen = "opencode-go/claude-haiku-5-5"
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("reason-legacy"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 4)
	claimReason(t, q, client, run, dispatcher.Reservation{ChosenModel: chosen})
	// The row as the earlier code left it: the model chosen, no reason.
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
		{Path: stateField, Value: stateQueued}, {Path: modelReasonField, Value: firestore.Delete},
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 18, 7, 42, 0, 0, time.UTC)
	if ok, _, err := q.TryClaim(ctx, run.RunID, at, generousBudget, openFacts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if got := reasonOf(t, client, run.RunID); got != (buildModelReason{Reason: chosenM}) {
		t.Fatalf("reason = %+v, want %q with no rates version", got, chosenM)
	}
}
