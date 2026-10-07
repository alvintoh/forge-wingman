package dispatcher

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// checkModels is the refusal a ticket's named models draw, empty when they may
// run. Each model must be well formed; the review model (the default when none
// is named, every entry of the default list) must differ from the build model
// the run will use; and each provider
// needs a plan record with its billing recorded, plus the owner's opt-in when it
// bills per token. The error is a failed plan read, which is not a refusal.
func checkModels(ctx context.Context, plans ModelPlans, m runner.ModelLabels, defaultModel string) (Refusal, string, error) {
	if reason, detail := malformedModel(m); reason != "" {
		return reason, detail, nil
	}
	build, review := cmp.Or(m.Build, defaultModel), []string{m.Review}
	if m.Review == "" {
		review = runner.DefaultReviewModels()
	}
	if slices.Contains(review, build) {
		return RefusalReviewIsBuild, "review model " + build + " is the build model", nil
	}
	seen := map[string]bool{}
	for _, model := range append([]string{m.Build, m.Review}, m.Plan...) {
		provider := runner.Provider(model)
		if model == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		plan, ok, err := plans.Plan(ctx, provider)
		if err != nil {
			return "", "", fmt.Errorf("reading the plan of provider %s: %w", provider, err)
		}
		switch {
		case !ok || !plan.Configured():
			return RefusalModelUnconfigured, "provider " + provider + " has no plan record with its billing recorded", nil
		case plan.NeedsOptIn() && !plan.OptedIn:
			return RefusalModelNotOptedIn, "provider " + provider + " bills per token and is not opted in (runner plan-optin)", nil
		}
	}
	return "", "", nil
}

// malformedModel is the refusal for a named model that is not provider/model,
// or a plan list that is empty-entried or repeats one.
func malformedModel(m runner.ModelLabels) (Refusal, string) {
	for _, one := range []struct{ prefix, model string }{{modelPrefix, m.Build}, {reviewModelPrefix, m.Review}} {
		if one.model != "" && !runner.ValidModel(one.model) {
			return RefusalModelMalformed, one.prefix + one.model + " is not provider/model"
		}
	}
	if len(m.Plan) > 0 {
		if err := runner.ValidatePlanModels(m.Plan); err != nil {
			return RefusalModelMalformed, err.Error()
		}
	}
	return "", ""
}
