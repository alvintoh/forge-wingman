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
		"a reply with no verdict": {[]string{"plan-reply", "-provider", "opencode-go", "-reply", "fine"}, "is not allowed, restricted or unconfirmed"},
		"a verdict with an impossible date": {[]string{"plan-verdict", "-provider", "opencode-go", "-verdict", "allowed",
			"-wording", "w", "-source", "https://v.example/terms", "-read-on", "2026-02-30"}, "yyyy-mm-dd"},
		"a definition with a malformed provider": {[]string{"plan-define", "-provider", "Open Code", "-name", "n", "-price-usd", "15"}, "is not a provider name"},
		"a definition with no price":             {[]string{"plan-define", "-provider", "opencode-go", "-name", "n"}, "not a positive amount"},
		"a definition with no billing":           {[]string{"plan-define", "-provider", "opencode-go", "-name", "n", "-price-usd", "15"}, "is not free, allowance or per-token"},
		"an opt-in naming a malformed provider":  {[]string{"plan-optin", "-provider", "Open Code"}, "is not a provider name"},
		"a refresh naming a malformed provider":  {[]string{"plans-refresh", "-provider", "Open Code"}, "is not a provider name"},
		"a refresh still given the feed flag":    {[]string{"plans-refresh", "-feed-url", "", "-provider", "Open Code"}, "is not a provider name"},
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
	for _, args := range [][]string{{"plan-list"}, {"plan-verdict", "-provider", "opencode-go"}, {"plan-optin", "-provider", "opencode-go"}, {"plans-refresh"}} {
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
	if got := harnessOptIn(context.Background(), logger, noEnv, []string{"opencode-go/deepseek-v4.1-flash"}); got != nil {
		t.Fatal("harnessOptIn returned a resolver for a command with no project to read a plan record from")
	}
	err := run(context.Background(), logger, []string{"plan-optin", "-provider", "opencode-go", "-private", "-harness", "vendor-cli"}, planEnv("octo"))
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

// stubPlanCaches hands plan-list its trailing runs' cache figures, none.
type stubPlanCaches struct{}

func (stubPlanCaches) PlanCaches(context.Context, time.Time) (map[string]store.PlanCache, error) {
	return nil, nil
}

// The ladder the plan-list tests configure, cheapest first, and the plan it is for.
const ladderPlan = "tiered"

var ladderTiers = []string{"tiered/cheap", "tiered/mid", "tiered/top"}

func tieredOf(plan string) []string {
	if plan == ladderPlan {
		return ladderTiers
	}
	return nil
}

// rankedSize is one size's ranking with the models clearing the bar, in order.
func rankedSize(size string, models ...string) providers.SizeRanking {
	row := providers.SizeRanking{Size: size}
	for _, m := range models {
		row.Ranked = append(row.Ranked, providers.RankedModel{Model: m, Settled: 5, Passed: 5})
	}
	return row
}

// belowBar adds models to size's below-bar list.
func belowBar(size providers.SizeRanking, models ...string) providers.SizeRanking {
	for _, m := range models {
		size.BelowBar = append(size.BelowBar, providers.RankedModel{Model: m, Settled: 5, Passed: 1})
	}
	return size
}

// rankedFacts is the facts of a plan read on readOn whose ranking was last good
// on goodOn, in state.
func rankedFacts(readOn, goodOn string, state providers.ReadState, sizes ...providers.SizeRanking) providers.Facts {
	return providers.Facts{ReadOn: readOn, Ranking: providers.RankingFacts{
		Source: providers.Source{State: state, LastGoodReadOn: goodOn}, Sizes: sizes}}
}

// listPlans runs plan-list's command function over the plans named in facts, in
// the order given, and returns what it printed and what it asked of the store.
func listPlans(t *testing.T, ladderOf func(string) []string, order []string, facts map[string]providers.Facts) (string, []string) {
	t.Helper()
	plans := &recordingPlans{facts: facts}
	for _, name := range order {
		plans.rows = append(plans.rows, providers.Listing{Provider: name})
	}
	var out strings.Builder
	if err := runPlanList(context.Background(), &out, plans, stubPlanCaches{}, nil, ladderOf, time.Now()); err != nil {
		t.Fatal(err)
	}
	return out.String(), plans.calls
}

// listTiered is listPlans over the one plan with a ladder.
func listTiered(t *testing.T, facts providers.Facts) (string, []string) {
	t.Helper()
	return listPlans(t, tieredOf, []string{ladderPlan}, map[string]providers.Facts{ladderPlan: facts})
}

// TestPlanListComparesRungOneWithTheTopRankedModelOfEachSize asserts a size whose
// top ranked model is not the ladder's cheapest is printed with both, a size
// whose top ranked model is the cheapest prints no rung 1 line, and the store is
// only read.
func TestPlanListComparesRungOneWithTheTopRankedModelOfEachSize(t *testing.T) {
	facts := rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged,
		rankedSize("S", "tiered/cheap", "tiered/mid"),
		rankedSize("M", "tiered/other", "tiered/cheap"))
	out, calls := listTiered(t, facts)
	if !strings.Contains(out, "tiered: ranking (ranked on 2026-11-22) differs from the ladder") ||
		!strings.Contains(out, "  size M rung 1: configured tiered/cheap, ranked top tiered/other\n") {
		t.Errorf("output lacks the rung 1 difference for M:\n%s", out)
	}
	if strings.Contains(out, "size S rung 1") {
		t.Errorf("output prints rung 1 for S, whose top ranked model is the cheapest:\n%s", out)
	}
	if !slices.Equal(calls, []string{"list", "latest tiered"}) {
		t.Errorf("store calls = %v, want only reads", calls)
	}
}

// TestPlanListSaysWhereEachHigherRungStandsInEachRanking asserts every ladder
// model above rung 1 gets one line with its place for each size: ordinals
// including the teens, below the bar, and not ranked, with no verdict.
func TestPlanListSaysWhereEachHigherRungStandsInEachRanking(t *testing.T) {
	var filler []string
	for i := 0; i < 11; i++ {
		filler = append(filler, "tiered/f"+strconv.Itoa(i))
	}
	ladder := func(string) []string { return []string{"tiered/cheap", "tiered/mid", "tiered/top", "tiered/deep"} }
	facts := rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged,
		rankedSize("S", "tiered/other", "tiered/mid", "tiered/top"),
		belowBar(rankedSize("M", "tiered/cheap", "tiered/mid"), "tiered/top"),
		rankedSize("L", append(filler, "tiered/deep")...))
	out, _ := listPlans(t, ladder, []string{ladderPlan}, map[string]providers.Facts{ladderPlan: facts})
	for _, want := range []string{
		"  rung 2 tiered/mid: ranked 2nd for S, ranked 2nd for M, not ranked for L\n",
		"  rung 3 tiered/top: ranked 3rd for S, below the bar for M, not ranked for L\n",
		"  rung 4 tiered/deep: not ranked for S, not ranked for M, ranked 12th for L\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestPlanListNamesASizeWhereNoModelClearedTheBarOnce asserts a size with no
// ranked model gets one line however long the ladder, while a size with one
// still reports its own rung 1.
func TestPlanListNamesASizeWhereNoModelClearedTheBarOnce(t *testing.T) {
	facts := rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged,
		rankedSize("S", "tiered/cheap"), rankedSize("M", "tiered/cheap"), rankedSize("L"))
	out, _ := listTiered(t, facts)
	if strings.Count(out, "size L: no model cleared the bar") != 1 || strings.Contains(out, "size L rung") {
		t.Errorf("output does not name size L once:\n%s", out)
	}
	if strings.Contains(out, "size S rung 1") || strings.Contains(out, "size M rung 1") {
		t.Errorf("output prints rung 1 for sizes that agree:\n%s", out)
	}
}

// TestPlanListSaysOnceWhenNoModelHasClearedTheBar asserts a ranking whose every
// size is empty prints one line, and neither the per-size lines nor a rung.
func TestPlanListSaysOnceWhenNoModelHasClearedTheBar(t *testing.T) {
	facts := rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged, rankedSize("S"), rankedSize("M"), rankedSize("L"))
	out, _ := listTiered(t, facts)
	want := "tiered: ranking (ranked on 2026-11-22): no model has cleared the bar yet\n"
	if !strings.HasSuffix(out, want) || strings.Contains(out, "rung") || strings.Contains(out, "size") {
		t.Fatalf("output = %q, want it to end with the one line %q", out, want)
	}
}

// TestPlanListSaysOnceWhenTheRankingMatchesTheLadder asserts a top ranked model
// equal to rung 1 for every size prints one line and no rung, whatever else is
// ranked, and that the line says rungs 2 and up are positions only.
func TestPlanListSaysOnceWhenTheRankingMatchesTheLadder(t *testing.T) {
	facts := rankedFacts("2026-11-22", "2026-11-22", providers.ReadUnchanged,
		rankedSize("S", "tiered/cheap", "tiered/other", "tiered/mid"),
		rankedSize("M", "tiered/cheap"))
	out, _ := listTiered(t, facts)
	if !strings.Contains(out, "tiered: ranking (ranked on 2026-11-22) matches the ladder at rung 1; rungs 2 and up are positions only\n") ||
		strings.Contains(out, "size") || strings.Contains(out, "rung 2") {
		t.Fatalf("output = %q, want one matching line and no size or rung line", out)
	}
}

// TestPlanListSaysOnceWhenAPlanHasNoLadder asserts a plan with no ladder prints
// one line, even beside a ranking, and its facts are not read.
func TestPlanListSaysOnceWhenAPlanHasNoLadder(t *testing.T) {
	facts := rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged, rankedSize("M", "tiered/mid"))
	out, calls := listPlans(t, func(string) []string { return nil }, []string{ladderPlan}, map[string]providers.Facts{ladderPlan: facts})
	if !strings.Contains(out, "tiered: no escalation ladder to compare\n") || strings.Contains(out, "rung") {
		t.Fatalf("output = %q, want one no-ladder line and no rung", out)
	}
	if !slices.Equal(calls, []string{"list"}) {
		t.Errorf("store calls = %v, want the facts unread", calls)
	}
}

// TestPlanListShowsTheDateACarriedForwardRankingWasRankedOn asserts a ranking the
// latest read could not refresh shows the earlier date it was ranked on, not the
// date of the facts that carry it.
func TestPlanListShowsTheDateACarriedForwardRankingWasRankedOn(t *testing.T) {
	facts := rankedFacts("2026-11-22", "2026-11-08", providers.ReadCouldNotRead, rankedSize("M", "tiered/top"))
	out, _ := listTiered(t, facts)
	if !strings.Contains(out, "tiered: ranking (ranked on 2026-11-08, carried forward) differs from the ladder") || strings.Contains(out, "2026-11-22") {
		t.Fatalf("output = %q, want the ranked-on date of 2026-11-08 marked carried forward", out)
	}
}

// TestPlanListSaysWhenNoRankingWasRecorded asserts facts with no ranking are not
// read as a ranking equal to the ladder.
func TestPlanListSaysWhenNoRankingWasRecorded(t *testing.T) {
	out, _ := listTiered(t, providers.Facts{ReadOn: "2026-11-22"})
	if !strings.Contains(out, "tiered: no ranking recorded\n") || strings.Contains(out, "matches") {
		t.Fatalf("output = %q, want one no-ranking line", out)
	}
}

// TestPlanListSeparatesTheReportFromTheTableAndKeepsPlanOrder asserts the report
// follows the table after one blank line, lists plans in the store's order, and
// prints no blank line when there are no plans.
func TestPlanListSeparatesTheReportFromTheTableAndKeepsPlanOrder(t *testing.T) {
	ladder := func(plan string) []string {
		if plan == "none" {
			return nil
		}
		return ladderTiers
	}
	facts := map[string]providers.Facts{"alpha": rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged, rankedSize("M", "tiered/other"))}
	out, _ := listPlans(t, ladder, []string{"alpha", "none"}, facts)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 5 || lines[3] != "" || !strings.HasPrefix(lines[4], "alpha: ranking") || lines[len(lines)-1] != "none: no escalation ladder to compare" {
		t.Fatalf("output = %q, want the table, one blank line, then alpha's report and none's line last", out)
	}
	empty, _ := listPlans(t, ladder, nil, nil)
	if strings.Contains(empty, "\n\n") {
		t.Fatalf("output = %q, want no blank line with no plans", empty)
	}
}

// failingFacts is a plan store whose facts cannot be read for one plan.
type failingFacts struct {
	*recordingPlans
	failing string
}

func (f failingFacts) LatestFacts(ctx context.Context, provider string) (providers.Facts, string, bool, error) {
	if provider == f.failing {
		return providers.Facts{}, "", false, errors.New("unavailable")
	}
	return f.recordingPlans.LatestFacts(ctx, provider)
}

// TestPlanListReportsTheOtherPlansWhenOnesFactsCannotBeRead asserts a plan whose
// facts cannot be read is named unreadable, the plans after it are still
// reported, and the error returned names it.
func TestPlanListReportsTheOtherPlansWhenOnesFactsCannotBeRead(t *testing.T) {
	plans := failingFacts{failing: "alpha", recordingPlans: &recordingPlans{
		rows:  []providers.Listing{{Provider: "alpha"}, {Provider: "beta"}},
		facts: map[string]providers.Facts{"beta": rankedFacts("2026-11-22", "2026-11-22", providers.ReadChanged, rankedSize("M", "tiered/other"))},
	}}
	var out strings.Builder
	err := runPlanList(context.Background(), &out, plans, stubPlanCaches{}, nil, func(string) []string { return ladderTiers }, time.Now())
	if err == nil || !strings.Contains(err.Error(), "alpha") || strings.Contains(err.Error(), "beta") {
		t.Fatalf("err = %v, want it to name alpha alone", err)
	}
	if !strings.Contains(out.String(), "alpha: ranking unreadable\n") || !strings.Contains(out.String(), "beta: ranking (ranked on 2026-11-22) differs from the ladder") {
		t.Fatalf("output = %q, want alpha unreadable and beta still reported", out.String())
	}
}
