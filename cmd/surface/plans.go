package main

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// plansReadTimeout bounds the reads one plans request makes.
const plansReadTimeout = 10 * time.Second

// PlanReader reads a provider's plan record and the facts last recorded for it.
type PlanReader interface {
	Plan(ctx context.Context, provider string) (plan providers.Plan, ok bool, err error)
	LatestFacts(ctx context.Context, provider string) (facts providers.Facts, date string, ok bool, err error)
}

// RequestShapeReader reads each stage's typical request.
type RequestShapeReader interface {
	RequestShapes(ctx context.Context) ([]store.StageShape, error)
}

// plansResponse is the plans endpoint's wire shape. Plans carry the comparison's
// own fields, so an unknown value is the word "unconfirmed". Model and Stage
// echo the figure's request, and are null when none was asked for.
type plansResponse struct {
	Plans  []providers.PlanComparison `json:"plans"`
	Shapes []stageShape               `json:"shapes"`
	Model  *string                    `json:"model"`
	Stage  *string                    `json:"stage"`
}

type stageShape struct {
	Stage string `json:"stage"`
	// Runs is how many runs the stage's typical request was read from; Shape is
	// null when too few recorded it.
	Runs  int           `json:"runs"`
	Shape *requestShape `json:"shape"`
}

type requestShape struct {
	Fresh  int64 `json:"fresh"`
	Cached int64 `json:"cached"`
	Output int64 `json:"output"`
}

// plansQuery is what a request asks the figures to be priced at: nothing, or a
// model and a stage together.
type plansQuery struct {
	asked bool
	model string
	stage runner.Phase
}

// plans serves GET /api/plans: every configured plan's comparison, each stage's
// typical request, and, when a model and a stage are given, each plan's requests
// per dollar at that stage's recorded request. A caller never supplies token
// counts, so the figure is measured and never marked estimated.
func (d Deps) plans(w http.ResponseWriter, r *http.Request) {
	if d.Plans == nil || d.Shapes == nil {
		http.Error(w, "the plans need a store", http.StatusServiceUnavailable)
		return
	}
	query, refusal := parsePlansQuery(r.URL.Query())
	if refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), plansReadTimeout)
	defer cancel()
	body, err := d.readPlans(ctx, query)
	if err != nil {
		d.Logger.Error("plansNotRead", "err", err.Error())
		http.Error(w, "reading the plans", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// parsePlansQuery reads the figure's model and stage, and says why a request is
// refused. A model or a stage given alone fails on the one that is missing. A
// refusal never repeats the query's text.
func parsePlansQuery(q url.Values) (plansQuery, string) {
	if !q.Has("model") && !q.Has("stage") {
		return plansQuery{}, ""
	}
	model := q.Get("model")
	if _, priced := providers.RatesFor(model); !priced {
		return plansQuery{}, "the model is not one a plan prices"
	}
	for _, stage := range store.RequestStages() {
		if q.Get("stage") == string(stage) {
			return plansQuery{asked: true, model: model, stage: stage}, ""
		}
	}
	return plansQuery{}, "the stage is not plan, build or review"
}

// readPlans reads everything the response holds before any of it is written, so
// a failing read leaves no partial answer.
func (d Deps) readPlans(ctx context.Context, query plansQuery) (plansResponse, error) {
	shapes, err := d.Shapes.RequestShapes(ctx)
	if err != nil {
		return plansResponse{}, err
	}
	in := providers.ComparisonInput{
		Plans:   map[string]providers.Plan{},
		Facts:   map[string]providers.Facts{},
		Harness: harnessCapabilities(),
	}
	for _, plan := range providers.Plans() {
		record, ok, err := d.Plans.Plan(ctx, plan)
		if err != nil {
			return plansResponse{}, err
		}
		if ok {
			in.Plans[plan] = record
		}
		facts, _, ok, err := d.Plans.LatestFacts(ctx, plan)
		if err != nil {
			return plansResponse{}, err
		}
		if ok {
			in.Facts[plan] = facts
		}
	}
	out := plansResponse{Shapes: make([]stageShape, 0, len(shapes))}
	for _, s := range shapes {
		shape := stageShape{Stage: string(s.Stage), Runs: s.Runs}
		if s.Shape != nil {
			shape.Shape = &requestShape{Fresh: s.Shape.Fresh, Cached: s.Shape.Cached, Output: s.Shape.Output}
		}
		out.Shapes = append(out.Shapes, shape)
		if query.asked && s.Stage == query.stage && s.Shape != nil {
			in.Request = providers.Request{Model: query.model, Fresh: s.Shape.Fresh, Cached: s.Shape.Cached, Output: s.Shape.Output}
		}
	}
	if query.asked {
		model, stage := query.model, string(query.stage)
		out.Model, out.Stage = &model, &stage
	}
	out.Plans = providers.ComparePlans(in)
	return out, nil
}

// harnessCapabilities reports what each registered harness does on a plan. A
// harness answers by its name and the plan alone, so none needs an environment.
func harnessCapabilities() func(harness, plan string) providers.HarnessCapabilities {
	byName := map[string]runner.Harness{}
	for _, h := range runner.Harnesses(func(string) string { return "" }) {
		byName[h.Name()] = h
	}
	return func(harness, plan string) providers.HarnessCapabilities {
		h, ok := byName[harness]
		if !ok {
			return providers.HarnessCapabilities{}
		}
		return h.Capabilities(plan)
	}
}
