package dispatcher

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fakeModelPlans holds the plan record of each provider and counts the reads made.
type fakeModelPlans struct {
	plans map[string]providers.Plan
	err   error
	reads []string
}

func (p *fakeModelPlans) Plan(_ context.Context, provider string) (providers.Plan, bool, error) {
	p.reads = append(p.reads, provider)
	if p.err != nil {
		return providers.Plan{}, false, p.err
	}
	plan, ok := p.plans[provider]
	return plan, ok, nil
}

func planBilled(b providers.Billing, optedIn bool) providers.Plan {
	return providers.Plan{Definition: providers.Definition{Billing: b}, OptedIn: optedIn}
}

var modelPlans = map[string]providers.Plan{
	"free":       planBilled(providers.BillingFree, false),
	"allowance":  planBilled(providers.BillingAllowance, false),
	"tokens":     planBilled(providers.BillingPerToken, false),
	"tokens-opt": planBilled(providers.BillingPerToken, true),
	"unrecorded": planBilled("", true),
}

const defaultBuild = "free/build"

func TestCheckModels(t *testing.T) {
	for name, tt := range map[string]struct {
		models runner.ModelLabels
		want   Refusal
		reads  []string
	}{
		"nothing named":                                  {runner.ModelLabels{}, "", nil},
		"a malformed build model":                        {runner.ModelLabels{Build: "build"}, RefusalModelMalformed, nil},
		"a malformed review model":                       {runner.ModelLabels{Review: "Free/x"}, RefusalModelMalformed, nil},
		"a plan list with an empty entry":                {runner.ModelLabels{Plan: []string{"free/a", ""}}, RefusalModelMalformed, nil},
		"a plan list repeating a model":                  {runner.ModelLabels{Plan: []string{"free/a", "free/a"}}, RefusalModelMalformed, nil},
		"a review model that is the build":               {runner.ModelLabels{Build: "free/a", Review: "free/a"}, RefusalReviewIsBuild, nil},
		"a review model that is the default build":       {runner.ModelLabels{Review: defaultBuild}, RefusalReviewIsBuild, nil},
		"a build model that is the default review model": {runner.ModelLabels{Build: runner.DefaultReviewModel}, RefusalReviewIsBuild, nil},
		"a provider with no plan record":                 {runner.ModelLabels{Build: "nobody/a"}, RefusalModelUnconfigured, []string{"nobody"}},
		"a plan with no billing recorded":                {runner.ModelLabels{Build: "unrecorded/a"}, RefusalModelUnconfigured, []string{"unrecorded"}},
		"a per-token build model not opted in":           {runner.ModelLabels{Build: "tokens/a"}, RefusalModelNotOptedIn, []string{"tokens"}},
		"a per-token review model not opted in":          {runner.ModelLabels{Review: "tokens/a"}, RefusalModelNotOptedIn, []string{"tokens"}},
		"a per-token plan backup not opted in": {runner.ModelLabels{Build: "free/a", Plan: []string{"allowance/b", "tokens/c"}},
			RefusalModelNotOptedIn, []string{"free", "allowance", "tokens"}},
		"a per-token provider the owner opted in": {runner.ModelLabels{Build: "tokens-opt/a"}, "", []string{"tokens-opt"}},
		"free and allowance providers":            {runner.ModelLabels{Build: "free/a", Review: "allowance/b"}, "", []string{"free", "allowance"}},
		"models sharing a provider":               {runner.ModelLabels{Build: "free/a", Review: "free/b", Plan: []string{"free/c"}}, "", []string{"free"}},
	} {
		t.Run(name, func(t *testing.T) {
			plans := &fakeModelPlans{plans: modelPlans}
			got, detail, err := checkModels(context.Background(), plans, tt.models, defaultBuild)
			if err != nil || got != tt.want {
				t.Fatalf("refusal %q (%s), err %v, want %q", got, detail, err, tt.want)
			}
			if (got == "") != (detail == "") {
				t.Fatalf("refusal %q with detail %q, want a detail exactly when refused", got, detail)
			}
			if !slices.Equal(plans.reads, tt.reads) {
				t.Fatalf("plans read %v, want %v", plans.reads, tt.reads)
			}
		})
	}
}

func TestCheckModelsNamesTheOptInAndTheProvider(t *testing.T) {
	_, detail, _ := checkModels(context.Background(), &fakeModelPlans{plans: modelPlans}, runner.ModelLabels{Build: "tokens/a"}, defaultBuild)
	if !strings.Contains(detail, "tokens") || !strings.Contains(detail, "opted in") {
		t.Fatalf("detail = %q, want it to name the provider and the missing opt-in", detail)
	}
}

func TestCheckModelsReportsAFailedReadAsAnErrorNotARefusal(t *testing.T) {
	plans := &fakeModelPlans{err: errFirestore}
	reason, _, err := checkModels(context.Background(), plans, runner.ModelLabels{Build: "free/a"}, defaultBuild)
	if err == nil || reason != "" {
		t.Fatalf("refusal %q, err %v, want an error and no refusal", reason, err)
	}
}

func TestLabelOverridesReadsEachNamedModel(t *testing.T) {
	for name, tt := range map[string]struct {
		labels []string
		want   runner.ModelLabels
	}{
		"no model label":     {[]string{"size:M", "repo:octo/scratch"}, runner.ModelLabels{}},
		"an empty value":     {[]string{"model:", "review-model:", "plan-model:"}, runner.ModelLabels{}},
		"each phase":         {[]string{"model:a/b", "review-model:c/d", "plan-model:e/f,g/h"}, runner.ModelLabels{Build: "a/b", Review: "c/d", Plan: []string{"e/f", "g/h"}}},
		"the first repeated": {[]string{"model:a/b", "model:c/d"}, runner.ModelLabels{Build: "a/b"}},
		"a trailing comma":   {[]string{"plan-model:e/f,"}, runner.ModelLabels{Plan: []string{"e/f", ""}}},
	} {
		t.Run(name, func(t *testing.T) {
			got := LabelOverrides{}.Overrides(admitted(tt.labels...))
			if got.Build != tt.want.Build || got.Review != tt.want.Review || !slices.Equal(got.Plan, tt.want.Plan) {
				t.Fatalf("overrides = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// fakeOverrides names the same models for every issue, whatever its labels.
type fakeOverrides runner.ModelLabels

func (o fakeOverrides) Overrides(Issue) runner.ModelLabels { return runner.ModelLabels(o) }

func modelPoll(t *testing.T, overrides ModelOverrides, plans *fakeModelPlans, q *fakeQueue, logs *bytes.Buffer, labels ...string) (Result, error) {
	t.Helper()
	d := pollDeps(fakeSource{issues: []Issue{admitted(labels...)}}, q, &fakeWorkflow{})
	d.Overrides = overrides
	d.ModelPlans = plans
	if logs != nil {
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	c := buildConfig
	c.Model = defaultBuild
	return Poll(context.Background(), d, c)
}

func TestPollQueuesTheRunWithTheModelsTheTicketNamed(t *testing.T) {
	q := &fakeQueue{}
	res, err := modelPoll(t, LabelOverrides{}, &fakeModelPlans{plans: modelPlans}, q, nil,
		"size:M", "repo:octo/scratch", "model:free/a", "review-model:allowance/b", "plan-model:free/c,allowance/d")
	if err != nil || len(res.Rejections) != 0 || len(q.queued) != 1 {
		t.Fatalf("result %+v, err %v, queued %d, want the ticket queued", res, err, len(q.queued))
	}
	got := q.queued[0].Models
	if got.Build != "free/a" || got.Review != "allowance/b" || !slices.Equal(got.Plan, []string{"free/c", "allowance/d"}) {
		t.Fatalf("queued models = %+v", got)
	}
}

func TestPollQueuesATicketNamingNoModelWithoutReadingAPlan(t *testing.T) {
	q, plans := &fakeQueue{}, &fakeModelPlans{plans: modelPlans}
	if _, err := modelPoll(t, LabelOverrides{}, plans, q, nil, "size:M", "repo:octo/scratch"); err != nil {
		t.Fatal(err)
	}
	if len(q.queued) != 1 || q.queued[0].Models.Build != "" || q.queued[0].Models.Review != "" || q.queued[0].Models.Plan != nil {
		t.Fatalf("queued = %+v, want a run with no named model", q.queued)
	}
	if len(plans.reads) != 0 {
		t.Fatalf("plans read %v, want none for a ticket naming no model", plans.reads)
	}
}

func TestPollRefusesATicketNamingAModelItMayNotUse(t *testing.T) {
	q := &fakeQueue{}
	res, err := modelPoll(t, LabelOverrides{}, &fakeModelPlans{plans: modelPlans}, q, nil, "size:M", "repo:octo/scratch", "model:tokens/a")
	if err != nil {
		t.Fatal(err)
	}
	if got := reasons(res.Rejections); !slices.Equal(got, []Refusal{RefusalModelNotOptedIn}) || len(q.queued) != 0 {
		t.Fatalf("refused %v, queued %d, want the opt-in refusal and nothing queued", got, len(q.queued))
	}
	if len(q.rejected) != 1 || q.rejected[0].Ticket != "FRG-18" {
		t.Fatalf("recorded %+v, want the refusal kept against the ticket", q.rejected)
	}
}

func TestPollRefusesAReviewModelThatIsTheDispatchersDefaultBuildModel(t *testing.T) {
	res, err := modelPoll(t, LabelOverrides{}, &fakeModelPlans{plans: modelPlans}, &fakeQueue{}, nil, "size:M", "repo:octo/scratch", "review-model:"+defaultBuild)
	if got := reasons(res.Rejections); err != nil || !slices.Equal(got, []Refusal{RefusalReviewIsBuild}) {
		t.Fatalf("refused %v, err %v, want the review-is-build refusal against the configured default model", got, err)
	}
}

func TestPollLeavesATicketUnadmittedWhenItsPlanCannotBeRead(t *testing.T) {
	var logs bytes.Buffer
	q := &fakeQueue{}
	res, err := modelPoll(t, LabelOverrides{}, &fakeModelPlans{err: errFirestore}, q, &logs, "size:M", "repo:octo/scratch", "model:tokens-opt/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.queued) != 0 || len(res.Rejections) != 0 || len(q.rejected) != 0 {
		t.Fatalf("queued %d, refused %v, want neither an admission nor a refusal", len(q.queued), res.Rejections)
	}
	if !strings.Contains(logs.String(), "modelConfigLookupFailed") || !strings.Contains(logs.String(), "run=FRG-18") {
		t.Fatalf("logs = %s, want modelConfigLookupFailed naming the run", logs.String())
	}
}

func TestPollChecksTheModelsAnyOverrideSourceNames(t *testing.T) {
	q := &fakeQueue{}
	res, err := modelPoll(t, fakeOverrides{Build: "tokens/a"}, &fakeModelPlans{plans: modelPlans}, q, nil, "size:M", "repo:octo/scratch")
	if err != nil {
		t.Fatal(err)
	}
	if got := reasons(res.Rejections); !slices.Equal(got, []Refusal{RefusalModelNotOptedIn}) {
		t.Fatalf("refused %v, want the opt-in refusal for a model no label named", got)
	}
}

func TestPollDoesNotRereadThePlansOfATicketAlreadyQueued(t *testing.T) {
	q, plans := &fakeQueue{holds: map[string]bool{"FRG-18": true}}, &fakeModelPlans{plans: modelPlans}
	if _, err := modelPoll(t, LabelOverrides{}, plans, q, nil, "size:M", "repo:octo/scratch", "model:free/a"); err != nil {
		t.Fatal(err)
	}
	if len(plans.reads) != 0 {
		t.Fatalf("plans read %v, want none for a ticket the queue already holds", plans.reads)
	}
}
