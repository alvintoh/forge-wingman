package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func planEnv(account string) func(string) string {
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": "/tmp", "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": account, "GITHUB_REPOSITORY_OWNER": "octo"}
	return func(k string) string { return env[k] }
}

func TestPlanCommandsRefuseInvalidInputBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"a reply with no verdict": {[]string{"plan-reply", "-provider", "command-code", "-reply", "fine"}, "is not allowed, restricted or unconfirmed"},
		"a verdict with an impossible date": {[]string{"plan-verdict", "-provider", "command-code", "-verdict", "allowed",
			"-wording", "w", "-source", "https://v.example/terms", "-read-on", "2026-02-30"}, "yyyy-mm-dd"},
		"a definition with a malformed provider": {[]string{"plan-define", "-provider", "Open Code", "-name", "n", "-price-usd", "15"}, "is not a provider name"},
		"a definition with no price":             {[]string{"plan-define", "-provider", "command-code", "-name", "n"}, "not a positive amount"},
		"a definition with no billing":           {[]string{"plan-define", "-provider", "command-code", "-name", "n", "-price-usd", "15"}, "is not free, allowance or per-token"},
		"an opt-in naming a malformed provider":  {[]string{"plan-optin", "-provider", "Open Code"}, "is not a provider name"},
		"a refresh naming a malformed provider":  {[]string{"plans-refresh", "-provider", "Open Code"}, "is not a provider name"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, planEnv("octo"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestPlanCommandsRefuseAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, args := range [][]string{{"plan-list"}, {"plan-verdict", "-provider", "command-code"}, {"plan-optin", "-provider", "command-code"}, {"plans-refresh"}} {
		err := run(context.Background(), logger, args, planEnv("work-account"))
		if !errors.Is(err, runner.ErrIdentityMismatch) {
			t.Errorf("%v: err = %v, want ErrIdentityMismatch", args, err)
		}
	}
}

func TestOpenPlansNeedsNoProviderForAListing(t *testing.T) {
	e := env{identity: runner.IdentityFromEnv(planEnv("octo"))}
	_, _, err := openPlans(context.Background(), e, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want it to get past the provider check and stop at the missing project", err)
	}
}

func TestPlanOptInPrivateRecordsOnlyThePrivateOptIn(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	provider := "plan-optin-private-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("provider_plans").Doc(provider).Delete(context.Background())
		_ = client.Close()
	})
	plans := store.NewPlans(client)
	if err := plans.PutDefinition(ctx, provider, providers.Definition{Name: "n", Billing: providers.BillingAllowance}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(ctx, logger, []string{"plan-optin", "-provider", provider, "-private"}, planEnv("octo")); err != nil {
		t.Fatal(err)
	}
	plan, _, err := plans.Plan(ctx, provider)
	if err != nil || !plan.PrivateOptIn || plan.OptedIn {
		t.Fatalf("plan %+v, err %v, want only the private opt-in recorded", plan, err)
	}
}

func TestTicketHandsTheBuildTheNamedModelsOfARowAnOldFreeTierClaimWrote(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "ticket-legacy-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	rec := runner.NewRecord(runID, runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}, time.Now())
	rec.ModelLabels = runner.ModelLabels{Build: "p/paid"}
	if err := store.NewRecords(client).PutRecord(ctx, runID, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection("runs").Doc(runID).Update(ctx, []firestore.Update{
		{Path: "last_resort", Value: true},
		{Path: "last_resort_models", Value: map[string]any{"build": "p/free-a", "plan": []string{"p/free-a"}}},
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": dir, "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": filepath.Join(dir, "output")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(ctx, logger, []string{"ticket", "-run-id", runID, "-out", filepath.Join(dir, "ticket.json")}, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(env["GITHUB_OUTPUT"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "override_model=p/paid") || strings.Contains(string(out), "p/free-a") {
		t.Fatalf("GITHUB_OUTPUT = %q, want the ticket's named build model", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ticket.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ParseTicket(string(raw)); err != nil {
		t.Fatalf("ticket file %q does not parse: %v", raw, err)
	}
}

func TestPlanOptInHarnessRecordsOnlyTheHarnessOptIn(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	provider := "plan-optin-harness-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("provider_plans").Doc(provider).Delete(context.Background())
		_ = client.Close()
	})
	plans := store.NewPlans(client)
	if err := plans.PutDefinition(ctx, provider, providers.Definition{Name: "n", Billing: providers.BillingAllowance,
		Harnesses: []providers.HarnessPair{{Harness: "vendor-cli", Model: "m"}}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(ctx, logger, []string{"plan-optin", "-provider", provider, "-harness", "vendor-cli"}, planEnv("octo")); err != nil {
		t.Fatal(err)
	}
	plan, _, err := plans.Plan(ctx, provider)
	if err != nil || !slices.Contains(plan.HarnessOptIn, "vendor-cli") || plan.OptedIn || plan.PrivateOptIn {
		t.Fatalf("plan %+v, err %v, want only the harness opt-in recorded", plan, err)
	}
}

// TestHarnessLinesListTheOpenSourcePairsFirst asserts the side-by-side view's
// pairs are ordered with the ones every run may select first, keeping the plan's
// own order within each group, and that a harness this build does not register is
// listed without a licence.
func TestHarnessLinesListTheOpenSourcePairsFirst(t *testing.T) {
	pairs := []providers.HarnessPair{
		{Harness: "vendor-cli", Model: "model-a"},
		{Harness: "open-cli", Model: "model-b"},
		{Harness: "unbuilt-cli", Model: "model-c"},
		{Harness: "newer-open-cli", Model: "model-d"},
	}
	licences := map[string]runner.Licence{
		"vendor-cli":     runner.LicenceProprietary,
		"open-cli":       runner.LicenceOpenSource,
		"newer-open-cli": runner.LicenceOpenSource,
	}
	got := harnessLines(pairs, licences)
	want := []providers.HarnessLine{
		{Harness: "open-cli", Model: "model-b", Licence: "open-source"},
		{Harness: "newer-open-cli", Model: "model-d", Licence: "open-source"},
		{Harness: "vendor-cli", Model: "model-a", Licence: "proprietary"},
		{Harness: "unbuilt-cli", Model: "model-c"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("harnessLines = %v, want %v", got, want)
	}
}

// TestHarnessOptInConsultsNoPlanRecordWithoutAProject asserts a command holding
// no project — every run job — reads no plan record, so no harness that needs an
// opt-in runs there, and that the two opt-ins cannot be asked for in one command.
func TestHarnessOptInConsultsNoPlanRecordWithoutAProject(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	noEnv := func(string) string { return "" }
	if got := harnessOptIn(context.Background(), logger, noEnv, []string{"command-code/deepseek/deepseek-v4.1-flash"}); got != nil {
		t.Fatal("harnessOptIn returned a resolver for a command with no project to read a plan record from")
	}
	err := run(context.Background(), logger, []string{"plan-optin", "-provider", "command-code", "-private", "-harness", "vendor-cli"}, planEnv("octo"))
	if err == nil || !strings.Contains(err.Error(), "separate opt-ins") {
		t.Fatalf("err = %v, want -private and -harness refused as one command", err)
	}
}

type fakePlanReader struct {
	reads map[string]int
	plans map[string]providers.Plan
}

func (f *fakePlanReader) Plan(_ context.Context, provider string) (providers.Plan, bool, error) {
	f.reads[provider]++
	plan, ok := f.plans[provider]
	if !ok {
		return providers.Plan{}, false, errors.New("unavailable")
	}
	return plan, true, nil
}

// TestOptInResolverReadsEachPlanOnceAndAdmitsNothingFromAnUnreadableOne asserts
// a plan whose record cannot be read is not re-read for each of its models and
// opts in no harness, while a readable plan's opt-in is honoured.
func TestOptInResolverReadsEachPlanOnceAndAdmitsNothingFromAnUnreadableOne(t *testing.T) {
	reader := &fakePlanReader{reads: map[string]int{}, plans: map[string]providers.Plan{
		"good": {HarnessOptIn: []string{"vendor-cli"}},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	optIn := optInResolver(context.Background(), logger, reader, []string{"bad/m1", "bad/m2", "good/m1", "good/m2"})
	if reader.reads["bad"] != 1 || reader.reads["good"] != 1 {
		t.Fatalf("reads = %v, want one read per plan", reader.reads)
	}
	if optIn("bad", "vendor-cli") || !optIn("good", "vendor-cli") {
		t.Fatal("opt-in did not follow the readable plan alone")
	}
}

// TestAttachPlanCachesJoinsByProvider asserts each row reads the figures of the
// plan its provider names, so a row never shows another plan's share.
func TestAttachPlanCachesJoinsByProvider(t *testing.T) {
	rows := []providers.Listing{{Provider: "alpha", Plan: providers.Plan{Definition: providers.Definition{Name: "beta"}}}, {Provider: "gamma"}}
	caches := map[string]store.PlanCache{
		"alpha": {string(runner.PhaseBuild): {FirstFresh: 10, FirstCached: 90, CachedShare: 0.75}},
		"beta":  {string(runner.PhaseBuild): {FirstFresh: 90, FirstCached: 10, CachedShare: 0.1}},
	}
	attachPlanCaches(rows, caches)
	if got, want := rows[0].BuildCache, "90% first, 75% phase"; got != want {
		t.Errorf("row alpha build = %q, want %q", got, want)
	}
	if got, want := rows[0].PlanCache, runner.PhaseCacheNone; got != want {
		t.Errorf("row alpha plan = %q, want %q", got, want)
	}
	if got, want := rows[1].BuildCache, runner.PhaseCacheNotRecorded; got != want {
		t.Errorf("a provider with no figures = %q, want %q", got, want)
	}
}
