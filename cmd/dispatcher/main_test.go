package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestLoadConfigReadsTheBoundaries(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{
		"GOOGLE_CLOUD_PROJECT": "forge-wingman",
		"LINEAR_DELEGATE":      "agent-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.project != "forge-wingman" || c.delegate != "agent-1" {
		t.Fatalf("config = %+v", c)
	}
}

func configEnv(extra map[string]string) func(string) string {
	kv := map[string]string{"GOOGLE_CLOUD_PROJECT": "forge-wingman", "LINEAR_DELEGATE": "agent-1"}
	for k, v := range extra {
		kv[k] = v
	}
	return envOf(kv)
}

// TestLoadConfigReadsTheDispatchSettings: the concurrency settings and the
// two-stage switch reach the poll's config, and an unusable one fails the load.
func TestLoadConfigReadsTheDispatchSettings(t *testing.T) {
	c, err := loadConfig(configEnv(map[string]string{"WINGMAN_REVIEW_WIP": "4", "WINGMAN_RISE_WITHIN": "1.1", "WINGMAN_TWO_STAGE": "on"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.limits.ReviewWIP != 4 || c.tuning.RiseWithin != 1.1 || !c.twoStage {
		t.Fatalf("config = %+v, want review WIP 4, rise band 1.1 and two-stage on", c)
	}
	if _, err := loadConfig(configEnv(map[string]string{"WINGMAN_REVIEW_WIP": "many"})); err == nil {
		t.Fatal("an unusable WINGMAN_REVIEW_WIP was accepted")
	}
}

func TestLoadConfigRefusesAnUnconfiguredJob(t *testing.T) {
	full := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "forge-wingman",
		"LINEAR_DELEGATE":      "agent-1",
	}
	for _, unset := range []string{"GOOGLE_CLOUD_PROJECT", "LINEAR_DELEGATE"} {
		t.Run(unset, func(t *testing.T) {
			kv := map[string]string{}
			for k, v := range full {
				kv[k] = v
			}
			delete(kv, unset)
			_, err := loadConfig(envOf(kv))
			if err == nil || !strings.Contains(err.Error(), unset) {
				t.Fatalf("err = %v, want it to name %s", err, unset)
			}
		})
	}
}

type fakeLister struct {
	repos []string
	err   error
}

func (f fakeLister) Repositories(context.Context) ([]string, error) { return f.repos, f.err }

func TestListedRepositoriesReturnsTheOwnersList(t *testing.T) {
	got, err := listedRepositories(context.Background(), fakeLister{repos: []string{"AlvinToh/Forge-Wingman", "octo/scratch"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"AlvinToh/Forge-Wingman", "octo/scratch"}) {
		t.Fatalf("repos = %v", got)
	}
}

func TestListedRepositoriesFailsTheWholeListOnOneBadEntry(t *testing.T) {
	got, err := listedRepositories(context.Background(), fakeLister{repos: []string{"octo/scratch", "octo"}})
	if err == nil || !strings.Contains(err.Error(), `"octo"`) || got != nil {
		t.Fatalf("got %v, %v, want no repositories and an error naming the entry", got, err)
	}
}

func TestListedRepositoriesReturnsAReadFailure(t *testing.T) {
	if _, err := listedRepositories(context.Background(), fakeLister{err: errors.New("owner database unreachable")}); err == nil {
		t.Fatal("an unreadable list was returned without an error")
	}
}

func TestSlackPosterReadsTheWebhookSecretTrimmed(t *testing.T) {
	token := func(_ context.Context, name string) (string, error) {
		if name != noticeWebhookSecret {
			t.Fatalf("read secret %q", name)
		}
		return "https://hooks.slack.com/services/T/B/x\n", nil
	}
	poster, err := slackPoster(token, nil)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := poster.(dispatcher.Slack).Webhook; got != "https://hooks.slack.com/services/T/B/x" {
		t.Fatalf("webhook = %q", got)
	}
}

func TestRunRefusesBeforeReadingTheTokens(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(context.Background(), logger, envOf(map[string]string{})); err == nil {
		t.Fatal("polled with nothing configured")
	}
}

// TestBudgetConfigCarriesTheProvidersWindows asserts the dispatcher meters the
// configured provider's own windows, not the cash ceiling alone.
func TestBudgetConfigCarriesTheProvidersWindows(t *testing.T) {
	provider := runner.Provider(runner.DefaultModel())
	want := providers.Windows(provider)
	got := budgetConfig(provider).ProviderWindows
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("windows = %+v, want the configured %+v", got, want)
	}
	for i, w := range want {
		if got[i].Name != w.Name || got[i].Period != w.Period || got[i].Limit != w.Limit {
			t.Errorf("window %d = %+v, want %+v", i, got[i], w)
		}
	}
	if b := budgetConfig("nobody"); len(b.ProviderWindows) != 0 || b.Cash.Limit != providers.CashLimit() {
		t.Errorf("unconfigured provider = %+v, want no windows and the cash ceiling", b)
	}
}

// TestBudgetConfigCarriesTheProvidersModelCaps asserts the dispatcher meters the
// configured provider's own per-model caps, so a run on a capped model is
// admitted only while that model has room too.
func TestBudgetConfigCarriesTheProvidersModelCaps(t *testing.T) {
	provider := runner.Provider(runner.DefaultModel())
	want := providers.ModelCaps(provider)
	got := budgetConfig(provider).ModelCaps
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("model caps = %+v, want the configured %+v", got, want)
	}
	for model, w := range want {
		g, ok := got[model]
		if !ok || g.Name != w.Name || g.Period != w.Period || g.Limit != w.Limit {
			t.Errorf("cap for %s = %+v, want %+v", model, g, w)
		}
	}
	if b := budgetConfig("nobody"); len(b.ModelCaps) != 0 {
		t.Errorf("unconfigured provider = %+v, want no model caps", b.ModelCaps)
	}
}

// TestLastResortPlansFollowTheConfiguredOrder asserts the dispatcher is handed
// each configured plan's free models in the configured order, so an exhausted
// run walks on to the next plan.
func TestLastResortPlansFollowTheConfiguredOrder(t *testing.T) {
	order := providers.LastResortPlans()
	rungs := lastResortPlans()
	if len(order) == 0 || len(rungs) != len(order) {
		t.Fatalf("rungs = %v, want one per configured plan %v", rungs, order)
	}
	for i, plan := range order {
		if want := providers.LastResortModels(plan); !slices.Equal(rungs[i], want) {
			t.Errorf("rung %d = %v, want %s's %v", i, rungs[i], plan, want)
		}
	}
}

func TestNextPlansIsEmptyForAnOrderOfOnePlanOrNone(t *testing.T) {
	for name, order := range map[string][]string{"none": nil, "one plan": {"command-code"}} {
		t.Run(name, func(t *testing.T) {
			next, err := nextPlans(order, runner.DefaultModel())
			if err != nil || len(next) != 0 {
				t.Fatalf("nextPlans = %v, %v, want none", next, err)
			}
		})
	}
}

func TestNextPlansListsEveryPlanAfterTheDefaultsWithItsOwnBudget(t *testing.T) {
	next, err := nextPlans([]string{"command-code", "gemini"}, runner.DefaultModel())
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Plan != "gemini" || next[0].Budget.Cash.Name != dispatcher.CeilingCash || len(next[0].Budget.ProviderWindows) != 0 {
		t.Fatalf("nextPlans = %+v, want gemini with its own budget, not command-code's windows", next)
	}
}

func TestNextPlansRefusesAnOrderNotStartingOnTheDefaultModelsPlan(t *testing.T) {
	_, err := nextPlans([]string{"gemini", "command-code"}, runner.DefaultModel())
	if err == nil || !strings.Contains(err.Error(), "the first paid plan is gemini, not command-code") {
		t.Fatalf("err = %v, want the first paid plan to be refused", err)
	}
}

func TestTheFirstPaidPlansModelsAreTheDefaultModels(t *testing.T) {
	m := providers.PlanModels(providers.PaidPlans()[0])
	if m.Build != runner.DefaultModel() || !slices.Equal(m.Plan, []string{runner.DefaultPlanModel()}) || m.Review != runner.DefaultReviewModels()[0] {
		t.Fatalf("plan models %+v differ from models.json's defaults", m)
	}
}

func TestLoadConfigReadsTheDecisionMode(t *testing.T) {
	for raw, want := range map[string]runner.DecisionMode{"": runner.DecisionGuided, "guided": runner.DecisionGuided, " autopilot ": runner.DecisionAutopilot} {
		c, err := loadConfig(configEnv(map[string]string{"WINGMAN_DECISIONS": raw}))
		if err != nil || c.decisionMode != want {
			t.Errorf("WINGMAN_DECISIONS=%q: mode = %q, err = %v, want %q", raw, c.decisionMode, err, want)
		}
	}
}

func TestLoadConfigRefusesAnUnknownDecisionMode(t *testing.T) {
	if _, err := loadConfig(configEnv(map[string]string{"WINGMAN_DECISIONS": "auto"})); !errors.Is(err, runner.ErrUnknownDecisionMode) {
		t.Fatalf("err = %v, want ErrUnknownDecisionMode", err)
	}
}

// stubRunsPerRepo is the owner's runs-per-repo cap as a poll reads it.
type stubRunsPerRepo struct {
	value int
	err   error
}

func (s stubRunsPerRepo) Value(context.Context) (int, error) { return s.value, s.err }

func TestOwnerRunsPerRepoReadsTheSavedCap(t *testing.T) {
	if got := ownerRunsPerRepo(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), stubRunsPerRepo{value: 8}); got != 8 {
		t.Fatalf("cap = %d, want the saved 8", got)
	}
}

// TestOwnerRunsPerRepoFallsBackToTheDefaultWhenItCannotBeRead is the rule that
// an unreadable cap is the default, never no cap at all.
func TestOwnerRunsPerRepoFallsBackToTheDefaultWhenItCannotBeRead(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	got := ownerRunsPerRepo(context.Background(), logger, stubRunsPerRepo{err: errors.New("no document")})
	if got != 5 {
		t.Fatalf("cap = %d, want the default 5", got)
	}
	if !strings.Contains(logs.String(), "runsPerRepoNotRead") {
		t.Fatalf("no runsPerRepoNotRead line in:\n%s", logs.String())
	}
}

// TestOwnerRunsPerRepoHoldsAnOutOfRangeCapToTheSettingsRange is the read that
// succeeds with a value the setting cannot hold: clamped to 1..20 rather than
// passed on, so no reader can lift the cap past 20 or leave it at zero or below.
func TestOwnerRunsPerRepoHoldsAnOutOfRangeCapToTheSettingsRange(t *testing.T) {
	for name, tt := range map[string]struct{ read, want int }{
		"zero":            {0, 1},
		"below the range": {-3, 1},
		"above the range": {21, 20},
		"far above":       {1000, 20},
	} {
		t.Run(name, func(t *testing.T) {
			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			if got := ownerRunsPerRepo(context.Background(), logger, stubRunsPerRepo{value: tt.read}); got != tt.want {
				t.Fatalf("cap = %d for a read of %d, want %d", got, tt.read, tt.want)
			}
			if !strings.Contains(logs.String(), "runsPerRepoOutOfRange") {
				t.Fatalf("no runsPerRepoOutOfRange line in:\n%s", logs.String())
			}
		})
	}
}

// TestOwnerRunsPerRepoLeavesAnInRangeCapAlone checks the ends of the range pass
// through unlogged.
func TestOwnerRunsPerRepoLeavesAnInRangeCapAlone(t *testing.T) {
	for _, n := range []int{1, 20} {
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		if got := ownerRunsPerRepo(context.Background(), logger, stubRunsPerRepo{value: n}); got != n || logs.Len() != 0 {
			t.Fatalf("cap = %d for a read of %d, log %q, want it unchanged and silent", got, n, logs.String())
		}
	}
}
