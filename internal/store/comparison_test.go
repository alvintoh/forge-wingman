package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// comparedRun is a settled run as the comparison reads it.
type comparedRun struct {
	plan, model, timeOfUse, costDrift string
	outcome                           runner.Outcome
	settledAt                         time.Time
	cost                              money.Micros
	lastResort                        bool
	// noSteps writes the document with no step at all, which Finalize never
	// produces (it settles only a run that reached an agent) and the
	// comparison still has to read.
	noSteps bool
}

// comparedRecord writes id as the run Finalize would leave it, with the
// comparison's own fields set.
func comparedRecord(t *testing.T, client *firestore.Client, id string, c comparedRun) {
	t.Helper()
	tk := runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "S", Body: "Add a file."}
	// A run that has not settled yet has no time of its own, and a zero one
	// would fall below Firestore's own range once the step's hour is taken off
	// it.
	started := queueAt.Add(-time.Hour)
	if !c.settledAt.IsZero() {
		started = c.settledAt.Add(-time.Hour)
	}
	rec := runner.NewRecord(id, tk, started)
	rec.Plan = c.plan
	rec.ModelLabels = runner.ModelLabels{Build: c.model}
	rec.LastResort = c.lastResort
	rec.LastResortModels = runner.ModelLabels{Build: c.model}
	rec.SettledAt = c.settledAt
	rec.SettledProviderCostMicros = c.cost
	rec.Outcome = c.outcome
	rec.TimeOfUse = c.timeOfUse
	rec.CostDrift = c.costDrift
	if !c.noSteps {
		rec.Steps = []runner.Step{{
			Phase: runner.PhaseBuild, Round: 1, Model: c.model,
			Tokens: runner.Usage{Cost: c.cost.USD()}, At: started,
		}}
	}
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), fields(rec)); err != nil {
		t.Fatal(err)
	}
}

func TestProviderTrailingCostIsTheMeanOfThatProvidersOwnSettledRuns(t *testing.T) {
	_, client := queue(t)
	plan, other := fresh("trail"), fresh("trail-other")
	a, b, c, elsewhere, unsettled, free := fresh("trail-a"), fresh("trail-b"), fresh("trail-c"),
		fresh("trail-away"), fresh("trail-unsettled"), fresh("trail-free")
	forget(t, client, a, b, c, elsewhere, unsettled, free)
	comparedRecord(t, client, a, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: money.Dollar})
	comparedRecord(t, client, b, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: 2 * money.Dollar})
	comparedRecord(t, client, c, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: 3 * money.Dollar})
	comparedRecord(t, client, elsewhere, comparedRun{plan: other, model: "command-code/x", settledAt: queueAt, cost: 100 * money.Dollar})
	comparedRecord(t, client, unsettled, comparedRun{plan: plan, model: "command-code/x", cost: 100 * money.Dollar})
	comparedRecord(t, client, free, comparedRun{plan: plan, model: "command-code/free", settledAt: queueAt, cost: 0, lastResort: true})

	mean, sample, err := NewRecords(client).ProviderTrailingCost(context.Background(), plan, queueAt.Add(-time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if mean != 2*money.Dollar || sample != 3 {
		t.Fatalf("trailing cost = %d micros over %d runs, want $2 over this provider's three settled runs", mean, sample)
	}
}

func TestProviderTrailingCostLeavesOutTheRunItCompares(t *testing.T) {
	_, client := queue(t)
	plan := fresh("trail-self")
	a, b, self := fresh("self-a"), fresh("self-b"), fresh("self-run")
	forget(t, client, a, b, self)
	comparedRecord(t, client, a, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: money.Dollar})
	comparedRecord(t, client, b, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: 2 * money.Dollar})
	comparedRecord(t, client, self, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: 6 * money.Dollar})

	mean, sample, err := NewRecords(client).ProviderTrailingCost(context.Background(), plan, queueAt.Add(-time.Hour), self)
	if err != nil {
		t.Fatal(err)
	}
	if mean != 3*money.Dollar/2 || sample != 2 {
		t.Fatalf("trailing cost = %d micros over %d runs, want $1.50 over the two runs it does not name", mean, sample)
	}
}

func TestProviderTrailingCostIsZeroWithoutAnyOfThatProvidersRuns(t *testing.T) {
	_, client := queue(t)
	free := fresh("trail-none")
	forget(t, client, free)
	comparedRecord(t, client, free, comparedRun{plan: fresh("trail-elsewhere"), model: "command-code/x", settledAt: queueAt, cost: money.Dollar})

	mean, sample, err := NewRecords(client).ProviderTrailingCost(context.Background(), fresh("trail-unseen"), queueAt.Add(-time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if mean != 0 || sample != 0 {
		t.Fatalf("trailing cost = %d micros over %d runs, want no average for a provider with no settled runs", mean, sample)
	}
}

func TestComparisonReportsEachModelsRunsAndEachProvidersTimeOfUse(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	plan, model, freeModel := fresh("cmp"), fresh("cmp-model"), fresh("cmp-free-model")
	passed, unchanged, failed, free := fresh("cmp-passed"), fresh("cmp-unchanged"), fresh("cmp-failed"), fresh("cmp-free")
	forget(t, client, passed, unchanged, failed, free)
	comparedRecord(t, client, passed, comparedRun{plan: plan, model: model, settledAt: queueAt,
		cost: money.Dollar, outcome: runner.OutcomePROpened, timeOfUse: runner.TimeOfUsePeak})
	comparedRecord(t, client, unchanged, comparedRun{plan: plan, model: model, settledAt: queueAt.Add(time.Minute),
		cost: 3 * money.Dollar, outcome: runner.OutcomeNoChanges, timeOfUse: runner.TimeOfUsePeak,
		costDrift: "cost $3.0000 is 50% above the $2.0000 average of 6 of this provider's runs"})
	comparedRecord(t, client, failed, comparedRun{plan: plan, model: model, settledAt: queueAt.Add(2 * time.Minute),
		cost: 2 * money.Dollar, outcome: runner.OutcomeAgentFailed, timeOfUse: runner.TimeOfUseOffPeak})
	comparedRecord(t, client, free, comparedRun{plan: plan, model: freeModel, settledAt: queueAt.Add(3 * time.Minute),
		cost: 0, outcome: runner.OutcomePROpened, lastResort: true})

	got, err := NewRecords(client).Comparison(ctx, queueAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := findModel(got.Models, model); !ok {
		t.Fatalf("models = %+v, want this test's model", got.Models)
	} else if m.Tickets != 3 || m.Passed != 2 || m.CostPerTicket != 2*money.Dollar {
		t.Fatalf("model = %+v, want three tickets, two passed, at $2 each", m)
	}
	// A free-tier run is a real run of its model, but its $0 says nothing of
	// what the provider charges, so it is the provider that leaves it out.
	if _, ok := findModel(got.Models, freeModel); !ok {
		t.Fatalf("models = %+v, want the free tier's own model", got.Models)
	}
	if p, ok := findProvider(got.Providers, plan); !ok {
		t.Fatalf("providers = %+v, want this test's provider", got.Providers)
	} else if p.Sample != 3 || p.TrailingCostPerTicket != 2*money.Dollar {
		t.Fatalf("provider = %+v, want the three paid runs at $2 each", p)
	} else if p.PeakTickets != 2 || p.PeakCost != 4*money.Dollar || p.OffPeakTickets != 1 || p.OffPeakCost != 2*money.Dollar {
		t.Fatalf("provider = %+v, want 2 peak runs at $4 and 1 off-peak at $2", p)
	}
	if len(got.Drifted) != 1 || got.Drifted[0].RunID != unchanged || got.Drifted[0].Provider != plan ||
		got.Drifted[0].Model != model || got.Drifted[0].Cost != 3*money.Dollar {
		t.Fatalf("drifted = %+v, want the flagged run alone", got.Drifted)
	}
	if !slices.IsSortedFunc(got.Models, func(a, b ModelComparison) int { return strings.Compare(a.Model, b.Model) }) ||
		!slices.IsSortedFunc(got.Providers, func(a, b ProviderComparison) int { return strings.Compare(a.Provider, b.Provider) }) {
		t.Fatalf("models %+v or providers %+v are not in a stable order", got.Models, got.Providers)
	}
}

func TestComparisonLeavesOutARunThatReachedNoAgent(t *testing.T) {
	_, client := queue(t)
	plan, ran, stopped := fresh("nosteps"), fresh("nosteps-ran"), fresh("nosteps-stopped")
	forget(t, client, ran, stopped)
	comparedRecord(t, client, ran, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: money.Dollar})
	comparedRecord(t, client, stopped, comparedRun{plan: plan, model: "command-code/x", settledAt: queueAt, cost: 100 * money.Dollar, noSteps: true})

	got, err := NewRecords(client).Comparison(context.Background(), queueAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "command-code/x" || got.Models[0].Tickets != 1 {
		t.Fatalf("models = %+v, want only the run that reached an agent", got.Models)
	}
	if p, ok := findProvider(got.Providers, plan); !ok {
		t.Fatalf("providers = %+v, want this test's provider", got.Providers)
	} else if p.Sample != 1 || p.TrailingCostPerTicket != money.Dollar {
		t.Fatalf("provider = %+v, want the one run that reached an agent counted once", p)
	}
}

func TestComparisonListsDriftedRunsNewestFirst(t *testing.T) {
	_, client := queue(t)
	older, newer := fresh("drift-older"), fresh("drift-newer")
	forget(t, client, older, newer)
	comparedRecord(t, client, older, comparedRun{plan: fresh("drift"), model: "command-code/x", settledAt: queueAt,
		cost: money.Dollar, costDrift: "cost $1.0000 is 100% above the $0.5000 average of 6 of this provider's runs"})
	comparedRecord(t, client, newer, comparedRun{plan: fresh("drift"), model: "command-code/x", settledAt: queueAt.Add(time.Minute),
		cost: 2 * money.Dollar, costDrift: "cost $2.0000 is 100% above the $1.0000 average of 6 of this provider's runs"})

	got, err := NewRecords(client).Comparison(context.Background(), queueAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(got.Drifted, func(d DriftedRun) bool { return d.RunID == newer })
	if at != 0 {
		t.Fatalf("drifted = %+v, want the newest flagged run first", got.Drifted)
	}
}

func findModel(models []ModelComparison, model string) (ModelComparison, bool) {
	if at := slices.IndexFunc(models, func(m ModelComparison) bool { return m.Model == model }); at >= 0 {
		return models[at], true
	}
	return ModelComparison{}, false
}

func findProvider(providers []ProviderComparison, provider string) (ProviderComparison, bool) {
	if at := slices.IndexFunc(providers, func(p ProviderComparison) bool { return p.Provider == provider }); at >= 0 {
		return providers[at], true
	}
	return ProviderComparison{}, false
}
