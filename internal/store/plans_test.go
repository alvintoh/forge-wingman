package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	prov "github.com/alvintoh/forge-wingman/internal/providers"
)

// planStore returns a Plans over the emulator, with a cleanup that removes the
// plans for ids and any facts written under them.
func planStore(t *testing.T, ids ...string) (*Plans, *firestore.Client) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	client, err := firestore.NewClient(context.Background(), "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range ids {
			ref := client.Collection(plansCollection).Doc(id)
			for _, date := range []string{"2026-09-20", "2026-09-27"} {
				_, _ = ref.Collection(factsCollection).Doc(date).Delete(ctx)
			}
			_, _ = ref.Delete(ctx)
		}
		_ = client.Close()
	})
	return NewPlans(client), client
}

func readPlan(t *testing.T, client *firestore.Client, id string) prov.Plan {
	t.Helper()
	snap, err := client.Collection(plansCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var plan prov.Plan
	if err := snap.DataTo(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

var planDef = prov.Definition{
	Name:           "Go Plan",
	MonthlyPrice:   15_500_000,
	Pages:          []prov.Locator{{URL: "https://vendor.example/pricing", Selector: "#plans"}},
	Harnesses:      []prov.HarnessPair{{Harness: "command-code", Model: "glm-5"}},
	LimitBehaviour: prov.LimitHardStop,
	Billing:        prov.BillingPerToken,
}

var planNote = prov.VerdictNote{Verdict: prov.VerdictRestricted, Wording: "no automated use", Source: "https://vendor.example/terms", ReadOn: "2026-09-30"}

func TestPutDefinitionKeepsTheVerdictAlreadyRecorded(t *testing.T) {
	id := fresh("plan-define")
	p, client := planStore(t, id)
	ctx := context.Background()
	if err := p.PutDefinition(ctx, id, planDef); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordVerdict(ctx, id, planNote); err != nil {
		t.Fatal(err)
	}
	redefined := planDef
	redefined.MonthlyPrice = 18_000_000
	if err := p.PutDefinition(ctx, id, redefined); err != nil {
		t.Fatal(err)
	}
	got := readPlan(t, client, id)
	if got.Definition.MonthlyPrice != 18_000_000 || got.Verdict != prov.VerdictRestricted || got.Wording != "no automated use" ||
		got.Source != "https://vendor.example/terms" || got.ReadOn != "2026-09-30" {
		t.Fatalf("plan = %+v, want the new price with the verdict and its evidence untouched", got)
	}
}

func TestRecordVerdictRefusesAPlanThatIsNotDefined(t *testing.T) {
	id := fresh("plan-undefined")
	p, _ := planStore(t, id)
	if err := p.RecordVerdict(context.Background(), id, planNote); err == nil || !strings.Contains(err.Error(), "is not defined") {
		t.Fatalf("err = %v, want a refusal naming the undefined plan", err)
	}
}

func TestAddReplyAppendsTheReplyAndSetsTheVerdictInOneWrite(t *testing.T) {
	id := fresh("plan-reply")
	p, client := planStore(t, id)
	ctx := context.Background()
	if err := p.PutDefinition(ctx, id, planDef); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	if err := p.AddReply(ctx, id, prov.Reply{Text: "agent use is fine", At: at}, prov.VerdictAllowed); err != nil {
		t.Fatal(err)
	}
	got := readPlan(t, client, id)
	if got.Verdict != prov.VerdictAllowed || len(got.Replies) != 1 || got.Replies[0].Text != "agent use is fine" || !got.Replies[0].At.Equal(at) {
		t.Fatalf("plan = %+v, want the reply stored beside verdict allowed", got)
	}
}

func TestAddReplyRefusesAPlanThatIsNotDefined(t *testing.T) {
	id := fresh("plan-reply-undefined")
	p, _ := planStore(t, id)
	if err := p.AddReply(context.Background(), id, prov.Reply{Text: "x"}, prov.VerdictAllowed); err == nil || !strings.Contains(err.Error(), "is not defined") {
		t.Fatalf("err = %v, want a refusal naming the undefined plan", err)
	}
}

func TestVerdictIsUnconfirmedUntilOneIsRecorded(t *testing.T) {
	undefined, defined := fresh("plan-none"), fresh("plan-bare")
	p, _ := planStore(t, undefined, defined)
	ctx := context.Background()
	if got, err := p.Verdict(ctx, undefined); err != nil || got != prov.VerdictUnconfirmed {
		t.Fatalf("no plan: verdict %q, err %v, want unconfirmed", got, err)
	}
	if err := p.PutDefinition(ctx, defined, planDef); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Verdict(ctx, defined); err != nil || got != prov.VerdictUnconfirmed {
		t.Fatalf("no verdict yet: verdict %q, err %v, want unconfirmed", got, err)
	}
	if err := p.RecordVerdict(ctx, defined, planNote); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Verdict(ctx, defined); err != nil || got != prov.VerdictRestricted {
		t.Fatalf("recorded: verdict %q, err %v, want restricted", got, err)
	}
}

func TestListReadsEachPlanWithItsLatestFactsDate(t *testing.T) {
	withFacts, without := fresh("plan-facts"), fresh("plan-nofacts")
	p, client := planStore(t, withFacts, without)
	ctx := context.Background()
	for _, id := range []string{withFacts, without} {
		if err := p.PutDefinition(ctx, id, planDef); err != nil {
			t.Fatal(err)
		}
	}
	for _, date := range []string{"2026-09-20", "2026-09-27"} {
		if _, err := client.Collection(plansCollection).Doc(withFacts).Collection(factsCollection).Doc(date).Set(ctx, map[string]any{"changes": []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Provider] = r.FactsDate
	}
	if d, ok := got[withFacts]; !ok || d != "2026-09-27" {
		t.Fatalf("facts date for %s = %q (listed %v), want 2026-09-27", withFacts, d, ok)
	}
	if d, ok := got[without]; !ok || d != "" {
		t.Fatalf("facts date for %s = %q (listed %v), want none", without, d, ok)
	}
}

func TestPlanReadsADefinedPlanAndReportsAnUndefinedOneAbsent(t *testing.T) {
	defined, undefined := fresh("plan-read"), fresh("plan-read-none")
	p, _ := planStore(t, defined, undefined)
	ctx := context.Background()
	if err := p.PutDefinition(ctx, defined, planDef); err != nil {
		t.Fatal(err)
	}
	got, ok, err := p.Plan(ctx, defined)
	if err != nil || !ok || got.Definition.Billing != prov.BillingPerToken || got.OptedIn {
		t.Fatalf("plan %+v, ok %v, err %v, want the defined per-token plan, not opted in", got, ok, err)
	}
	if _, ok, err := p.Plan(ctx, undefined); err != nil || ok {
		t.Fatalf("ok %v, err %v, want an undefined plan reported absent without an error", ok, err)
	}
}

func TestSetOptInSurvivesRedefiningThePlanAndRefusesAnUndefinedOne(t *testing.T) {
	id, undefined := fresh("plan-optin"), fresh("plan-optin-none")
	p, client := planStore(t, id, undefined)
	ctx := context.Background()
	if err := p.PutDefinition(ctx, id, planDef); err != nil {
		t.Fatal(err)
	}
	if err := p.SetOptIn(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := p.PutDefinition(ctx, id, planDef); err != nil {
		t.Fatal(err)
	}
	if got := readPlan(t, client, id); !got.OptedIn || got.Definition.Billing != prov.BillingPerToken {
		t.Fatalf("plan = %+v, want the opt-in kept through a re-definition", got)
	}
	if err := p.SetOptIn(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if got := readPlan(t, client, id); got.OptedIn {
		t.Fatalf("plan = %+v, want the opt-in withdrawn", got)
	}
	if err := p.SetOptIn(ctx, undefined, true); err == nil || !strings.Contains(err.Error(), "is not defined") {
		t.Fatalf("err = %v, want a refusal naming the undefined plan", err)
	}
}

func TestPutDefinitionWithdrawsTheOptInWhenBillingChanges(t *testing.T) {
	id := fresh("plan-billing")
	p, client := planStore(t, id)
	ctx := context.Background()
	if err := p.PutDefinition(ctx, id, planDef); err != nil {
		t.Fatal(err)
	}
	if err := p.SetOptIn(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	free := planDef
	free.Billing = prov.BillingFree
	if err := p.PutDefinition(ctx, id, free); err != nil {
		t.Fatal(err)
	}
	if got := readPlan(t, client, id); got.OptedIn || got.Definition.Billing != prov.BillingFree {
		t.Fatalf("plan = %+v, want the new billing with the opt-in withdrawn", got)
	}
	if err := p.PutDefinition(ctx, id, planDef); err != nil {
		t.Fatal(err)
	}
	if got := readPlan(t, client, id); got.OptedIn || got.Definition.Billing != prov.BillingPerToken {
		t.Fatalf("plan = %+v, want per-token again without the earlier opt-in", got)
	}
}
