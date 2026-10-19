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
		"WINGMAN_REPOS":        "octo/scratch, AlvinToh/Forge-Wingman ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.project != "forge-wingman" || c.delegate != "agent-1" {
		t.Fatalf("config = %+v", c)
	}
	if !slices.Equal(c.repos, []string{"octo/scratch", "AlvinToh/Forge-Wingman"}) {
		t.Fatalf("repos = %v", c.repos)
	}
}

func configEnv(extra map[string]string) func(string) string {
	kv := map[string]string{"GOOGLE_CLOUD_PROJECT": "forge-wingman", "LINEAR_DELEGATE": "agent-1", "WINGMAN_REPOS": "octo/scratch"}
	for k, v := range extra {
		kv[k] = v
	}
	return envOf(kv)
}

func TestLoadConfigReadsTheConcurrencySettings(t *testing.T) {
	c, err := loadConfig(configEnv(map[string]string{
		"WINGMAN_PLATFORM_CAP": " 8 ", "WINGMAN_LARGE_CAP": "2", "WINGMAN_REVIEW_WIP": "4",
		"WINGMAN_STABLE_RUNS": "3", "WINGMAN_RISE_WITHIN": " 1.1 ", "WINGMAN_HALVE_BEYOND": "1.8",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := (dispatcher.Limits{PlatformCap: 8, LargeCap: 2, ReviewWIP: 4}); c.limits != want {
		t.Fatalf("limits = %+v, want %+v", c.limits, want)
	}
	if want := (dispatcher.Tuning{StableRuns: 3, RiseWithin: 1.1, HalveBeyond: 1.8}); c.tuning != want {
		t.Fatalf("tuning = %+v, want %+v", c.tuning, want)
	}
}

func TestLoadConfigLeavesUnsetConcurrencySettingsToTheirDefaults(t *testing.T) {
	c, err := loadConfig(configEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.limits != (dispatcher.Limits{}) || c.tuning != (dispatcher.Tuning{}) {
		t.Fatalf("limits %+v tuning %+v, want zero so the defaults apply", c.limits, c.tuning)
	}
	if c.dailyRuns != 0 {
		t.Fatalf("dailyRuns = %d, want zero so the poll's default applies", c.dailyRuns)
	}
}

// TestLoadConfigReadsTheDailyRunCap: WINGMAN_DAILY_RUNS is a positive whole
// number the job reads like the caps.
func TestLoadConfigReadsTheDailyRunCap(t *testing.T) {
	c, err := loadConfig(configEnv(map[string]string{"WINGMAN_DAILY_RUNS": " 2 "}))
	if err != nil {
		t.Fatal(err)
	}
	if c.dailyRuns != 2 {
		t.Fatalf("dailyRuns = %d, want 2", c.dailyRuns)
	}
}

// TestLoadConfigRefusesAnUnusableDailyRunCap: a cap that is not a whole number,
// or one below one, is refused rather than quietly defaulted.
func TestLoadConfigRefusesAnUnusableDailyRunCap(t *testing.T) {
	for name, raw := range map[string]string{
		"not a whole number": "many",
		"below one":          "0",
		"negative":           "-3",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(configEnv(map[string]string{"WINGMAN_DAILY_RUNS": raw})); err == nil {
				t.Fatalf("WINGMAN_DAILY_RUNS = %q was accepted", raw)
			}
		})
	}
}

func TestLoadConfigRefusesUnusableConcurrencySettings(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"a zero cap":                              {"WINGMAN_PLATFORM_CAP": "0"},
		"a negative cap":                          {"WINGMAN_LARGE_CAP": "-1"},
		"a cap that is not a number":              {"WINGMAN_REVIEW_WIP": "many"},
		"a fractional count":                      {"WINGMAN_STABLE_RUNS": "2.5"},
		"a zero rise band":                        {"WINGMAN_RISE_WITHIN": "0"},
		"a rise band that is not a number":        {"WINGMAN_RISE_WITHIN": "NaN"},
		"an infinite fall band":                   {"WINGMAN_HALVE_BEYOND": "Inf"},
		"a fall band under the default rise band": {"WINGMAN_HALVE_BEYOND": "1.2"},
		"a rise band over the default fall band":  {"WINGMAN_RISE_WITHIN": "2"},
		"equal bands":                             {"WINGMAN_RISE_WITHIN": "1.4", "WINGMAN_HALVE_BEYOND": "1.4"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(configEnv(kv)); err == nil {
				t.Fatalf("%v was accepted", kv)
			}
		})
	}
}

func TestLoadConfigRefusesAnUnconfiguredJob(t *testing.T) {
	full := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "forge-wingman",
		"LINEAR_DELEGATE":      "agent-1",
		"WINGMAN_REPOS":        "octo/scratch",
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
	// An allowlist that names no repository would admit whatever a label names.
	for _, repos := range []string{"", "  ,  ", "octo"} {
		kv := map[string]string{}
		for k, v := range full {
			kv[k] = v
		}
		kv["WINGMAN_REPOS"] = repos
		if _, err := loadConfig(envOf(kv)); err == nil {
			t.Fatalf("WINGMAN_REPOS = %q was accepted", repos)
		}
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

func TestLoadConfigReadsTheTwoStageSwitch(t *testing.T) {
	for name, tt := range map[string]struct {
		value string
		want  bool
	}{
		"on":              {"on", true},
		"ON with padding": {"  On ", true},
		"unset":           {"", false},
		"off":             {"off", false},
		"anything else":   {"someday", false},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := loadConfig(configEnv(map[string]string{"WINGMAN_TWO_STAGE": tt.value}))
			if err != nil {
				t.Fatal(err)
			}
			if c.twoStage != tt.want {
				t.Fatalf("twoStage = %v for %q, want %v", c.twoStage, tt.value, tt.want)
			}
		})
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
