package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

const (
	plansTestModel  = "opencode-go/deepseek-v4.1-flash"
	plansTestSecret = "PRIVATE-WORDING"
)

// readLog records the time left on the context of each read a stub answers, and
// -1 for a read made with no deadline.
type readLog struct{ remaining []time.Duration }

func (l *readLog) note(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		l.remaining = append(l.remaining, -1)
		return
	}
	l.remaining = append(l.remaining, time.Until(deadline))
}

// stubPlans is a PlanReader over the records it holds, counting its reads.
type stubPlans struct {
	readLog
	plans    map[string]providers.Plan
	facts    map[string]providers.Facts
	planErr  error
	factsErr error
	reads    int
}

func (s *stubPlans) Plan(ctx context.Context, provider string) (providers.Plan, bool, error) {
	s.reads++
	s.note(ctx)
	plan, ok := s.plans[provider]
	return plan, ok, s.planErr
}

func (s *stubPlans) LatestFacts(ctx context.Context, provider string) (providers.Facts, string, bool, error) {
	s.reads++
	s.note(ctx)
	facts, ok := s.facts[provider]
	return facts, "2026-11-20", ok, s.factsErr
}

// stubShapes is a RequestShapeReader answering with the stage shapes it holds.
// With entered set it announces each read and then blocks until its context ends.
type stubShapes struct {
	readLog
	shapes  []store.StageShape
	err     error
	reads   int
	entered chan struct{}
}

func (s *stubShapes) RequestShapes(ctx context.Context) ([]store.StageShape, error) {
	s.reads++
	s.note(ctx)
	if s.entered != nil {
		s.entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.shapes, s.err
}

// recordedShapes holds a shape for the plan and review stages and too few runs
// for the build stage.
func recordedShapes() []store.StageShape {
	return []store.StageShape{
		{Stage: runner.PhasePlan, Runs: 7, Shape: &store.RequestShape{Fresh: 52000, Cached: 2200, Output: 1500}},
		{Stage: runner.PhaseBuild, Runs: 2},
		{Stage: runner.PhaseReview, Runs: 5, Shape: &store.RequestShape{Fresh: 20000, Cached: 100000, Output: 2000}},
	}
}

func recordedPlans() *stubPlans {
	return &stubPlans{
		plans: map[string]providers.Plan{"opencode-go": {
			Definition: providers.Definition{MonthlyPrice: 15 * money.Dollar},
			Verdict:    providers.VerdictAllowed,
			Wording:    plansTestSecret,
			Replies:    []providers.Reply{{Text: plansTestSecret}},
		}},
		facts: map[string]providers.Facts{"opencode-go": {
			Leaderboard: providers.Source{State: providers.ReadChanged},
			Models: providers.ModelsFacts{Models: []providers.ModelFacts{
				{Model: "opencode-go/muse", Leaderboard: providers.Score{Listed: true, Value: 70}},
			}},
		}},
	}
}

func servePlans(deps Deps, query string) *httptest.ResponseRecorder {
	deps.SPA, deps.Logger = fstest.MapFS{}, discardLogger()
	rec := httptest.NewRecorder()
	newMux(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plans"+query, nil))
	return rec
}

func figureQuery(model, stage string) string {
	return "?" + url.Values{"model": {model}, "stage": {stage}}.Encode()
}

// plansBody is the response with each plan's fields left as the JSON they were.
type plansBody struct {
	Plans  []map[string]json.RawMessage `json:"plans"`
	Shapes []struct {
		Stage string `json:"stage"`
		Runs  int    `json:"runs"`
		Shape *struct{ Fresh, Cached, Output int64 }
	} `json:"shapes"`
	Model *string `json:"model"`
	Stage *string `json:"stage"`
}

func getPlans(t *testing.T, plans *stubPlans, shapes *stubShapes, query string) plansBody {
	t.Helper()
	rec := servePlans(Deps{Plans: plans, Shapes: shapes}, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var body plansBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func (b plansBody) plan(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	for _, p := range b.Plans {
		if string(p["plan"]) == `"`+name+`"` {
			return p
		}
	}
	t.Fatalf("no plan %q in %d plans", name, len(b.Plans))
	return nil
}

func TestPlansWithoutAQueryServeEveryPlanAndStageShapeWithNoFigure(t *testing.T) {
	body := getPlans(t, recordedPlans(), &stubShapes{shapes: recordedShapes()}, "")

	if len(body.Plans) != 2 || string(body.Plans[0]["plan"]) != `"opencode-go"` || string(body.Plans[1]["plan"]) != `"gemini"` {
		t.Fatalf("plans = %v, want opencode-go then gemini", body.Plans)
	}
	paid, free := body.plan(t, "opencode-go"), body.plan(t, "gemini")
	if string(paid["monthly_price_micros"]) != "15000000" || string(paid["verdict"]) != `"allowed"` {
		t.Errorf("recorded price = %s, verdict = %s, want 15000000 and allowed", paid["monthly_price_micros"], paid["verdict"])
	}
	if !strings.Contains(string(paid["strongest"]), "opencode-go/muse") {
		t.Errorf("strongest = %s, want the model of the recorded leaderboard", paid["strongest"])
	}
	if string(free["monthly_price_micros"]) != `"unconfirmed"` || string(free["strongest"]) != `"unconfirmed"` {
		t.Errorf("unrecorded price = %s, strongest = %s, want the word unconfirmed", free["monthly_price_micros"], free["strongest"])
	}
	for _, p := range body.Plans {
		if string(p["requests_per_dollar"]) != `"unconfirmed"` {
			t.Errorf("%s figure = %s, want none without a query", p["plan"], p["requests_per_dollar"])
		}
	}
	if !strings.Contains(string(paid["harnesses"]), `"headless":true`) || !strings.Contains(string(paid["harnesses"]), `"denies_planning_edits":"unconfirmed"`) {
		t.Errorf("harnesses = %s, want the omp adapter's recorded capabilities beside the unconfirmed one", paid["harnesses"])
	}
	if body.Model != nil || body.Stage != nil {
		t.Errorf("echo = %v, %v, want null without a query", body.Model, body.Stage)
	}
	if len(body.Shapes) != 3 || body.Shapes[0].Stage != "plan" || body.Shapes[1].Stage != "build" || body.Shapes[2].Stage != "review" {
		t.Fatalf("shapes = %+v, want plan, build, review", body.Shapes)
	}
	if s := body.Shapes[0]; s.Runs != 7 || s.Shape == nil || *s.Shape != (struct{ Fresh, Cached, Output int64 }{52000, 2200, 1500}) {
		t.Errorf("plan shape = %+v, want 7 runs and the recorded request", s)
	}
	if s := body.Shapes[1]; s.Runs != 2 || s.Shape != nil {
		t.Errorf("build shape = %+v, want 2 runs and no shape", s)
	}
}

type plansTestFigure struct {
	OffPeak int64 `json:"off_peak"`
	HasPeak bool  `json:"has_peak"`
	Peak    int64 `json:"peak"`
	Shape   struct {
		Model                 string
		Fresh, Cached, Output int64
		Approximate           bool
	}
	Mark string
}

func plansTestFigureOf(t *testing.T, raw json.RawMessage) plansTestFigure {
	t.Helper()
	var f plansTestFigure
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("figure %s: %v", raw, err)
	}
	return f
}

func TestPlansPriceTheRequestAtTheStageTheCallerNames(t *testing.T) {
	for name, tt := range map[string]struct {
		stage                 string
		offPeak, peak         int64
		fresh, cached, output int64
	}{
		"plan":   {"plan", 114, 57, 52000, 2200, 1500},
		"review": {"review", 222, 111, 20000, 100000, 2000},
	} {
		t.Run(name, func(t *testing.T) {
			body := getPlans(t, recordedPlans(), &stubShapes{shapes: recordedShapes()}, figureQuery(plansTestModel, tt.stage))
			got := plansTestFigureOf(t, body.plan(t, "opencode-go")["requests_per_dollar"])
			if got.OffPeak != tt.offPeak || !got.HasPeak || got.Peak != tt.peak || got.Mark != "exact" {
				t.Errorf("figure = %+v, want %d off-peak and %d at peak, exact", got, tt.offPeak, tt.peak)
			}
			shape := got.Shape
			if shape.Model != plansTestModel || shape.Fresh != tt.fresh || shape.Cached != tt.cached || shape.Output != tt.output || shape.Approximate {
				t.Errorf("shape = %+v, want the %s stage's recorded request, measured", shape, tt.stage)
			}
			if string(body.plan(t, "gemini")["requests_per_dollar"]) != `"unconfirmed"` {
				t.Errorf("gemini figure = %s, want none: it prices nothing", body.plan(t, "gemini")["requests_per_dollar"])
			}
			if body.Model == nil || *body.Model != plansTestModel || body.Stage == nil || *body.Stage != tt.stage {
				t.Errorf("echo = %v, %v, want %s and %s", body.Model, body.Stage, plansTestModel, tt.stage)
			}
		})
	}
}

func TestPlansGiveNoFigureForAStageWithNoRecordedShape(t *testing.T) {
	body := getPlans(t, recordedPlans(), &stubShapes{shapes: recordedShapes()}, figureQuery(plansTestModel, "build"))
	for _, p := range body.Plans {
		if string(p["requests_per_dollar"]) != `"unconfirmed"` {
			t.Errorf("%s figure = %s, want none rather than a default", p["plan"], p["requests_per_dollar"])
		}
	}
	if body.Stage == nil || *body.Stage != "build" {
		t.Errorf("stage = %v, want it echoed", body.Stage)
	}
}

func TestPlansRefuseAQueryThatIsNotAPricedModelWithAStage(t *testing.T) {
	for name, query := range map[string]string{
		"unknown model":              figureQuery("opencode-go/not-a-model", "plan"),
		"empty model":                figureQuery("", "plan"),
		"another plan's prefix only": figureQuery("opencode-go/", "plan"),
		"markup in the model":        figureQuery("<script>alert(1)</script>", "plan"),
		"a phase that is no stage":   figureQuery(plansTestModel, "fix"),
		"a stage in capitals":        figureQuery(plansTestModel, "Plan"),
		"empty stage":                figureQuery(plansTestModel, ""),
		"markup in the stage":        figureQuery(plansTestModel, `plan","model":"x`),
		"model alone":                "?model=" + url.QueryEscape(plansTestModel),
		"stage alone":                "?stage=plan",
		"both given empty":           "?model=&stage=",
		"model key alone, empty":     "?model=",
		"stage key alone, empty":     "?stage=",
	} {
		t.Run(name, func(t *testing.T) {
			plans, shapes := recordedPlans(), &stubShapes{shapes: recordedShapes()}
			rec := servePlans(Deps{Plans: plans, Shapes: shapes}, query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(body, "script") || strings.Contains(body, "alert") || strings.Contains(body, `"x"`) || strings.Contains(body, "not-a-model") {
				t.Errorf("body = %q, want the refusal to leave the query's text out", body)
			}
			if plans.reads != 0 || shapes.reads != 0 {
				t.Errorf("reads = %d plans, %d shapes, want none before a query is accepted", plans.reads, shapes.reads)
			}
		})
	}
}

func TestPlansAnswerAnErrorAndNoPlansWhenAReadFails(t *testing.T) {
	boom := errors.New("firestore is down")
	for name, setup := range map[string]func(*stubPlans, *stubShapes){
		"the shapes": func(_ *stubPlans, s *stubShapes) { s.err = boom },
		"a plan":     func(p *stubPlans, _ *stubShapes) { p.planErr = boom },
		"the facts":  func(p *stubPlans, _ *stubShapes) { p.factsErr = boom },
	} {
		t.Run(name, func(t *testing.T) {
			plans, shapes := recordedPlans(), &stubShapes{shapes: recordedShapes()}
			setup(plans, shapes)
			rec := servePlans(Deps{Plans: plans, Shapes: shapes}, "")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if got := rec.Body.String(); got != "reading the plans\n" {
				t.Errorf("body = %q, want the refusal alone with no plan in it", got)
			}
		})
	}
}

func TestPlansNeedBothStores(t *testing.T) {
	for name, deps := range map[string]Deps{
		"no plan store":  {Shapes: &stubShapes{}},
		"no shape store": {Plans: &stubPlans{}},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := servePlans(deps, ""); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
		})
	}
}

func TestPlansServeOnlyTheFieldsTheComparisonDerives(t *testing.T) {
	rec := servePlans(Deps{Plans: recordedPlans(), Shapes: &stubShapes{shapes: recordedShapes()}}, "")
	if body := rec.Body.String(); strings.Contains(body, plansTestSecret) || strings.Contains(body, "replies") || strings.Contains(body, "opted_in") {
		t.Fatalf("body carries the owner's plan record beyond the comparison: %s", body)
	}
}

func TestPlansEveryReadRunsUnderADeadlineOfTenSeconds(t *testing.T) {
	plans, shapes := recordedPlans(), &stubShapes{shapes: recordedShapes()}
	getPlans(t, plans, shapes, "")

	// One shape read, then a plan and a facts read for each of the two configured plans.
	if len(shapes.remaining) != 1 || len(plans.remaining) != 4 {
		t.Fatalf("reads = %d shapes, %d plans, want 1 and 4", len(shapes.remaining), len(plans.remaining))
	}
	for _, left := range append(shapes.remaining, plans.remaining...) {
		if left <= 0 || left > 10*time.Second {
			t.Errorf("time left on a read = %v, want within (0, 10s]", left)
		}
	}
}

func TestPlansCancelTheirReadsWhenTheRequestEnds(t *testing.T) {
	shapes := &stubShapes{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	deps := Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Plans: recordedPlans(), Shapes: shapes}
	served := make(chan struct{})
	go func() {
		newMux(deps).ServeHTTP(rec, req)
		close(served)
	}()

	<-shapes.entered
	cancel()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler kept waiting on a read after its request ended")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestPlansAreServedOnlyToASignedInRequest(t *testing.T) {
	deps := Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Plans: recordedPlans(), Shapes: &stubShapes{shapes: recordedShapes()}}

	t.Setenv("WINGMAN_LOCAL_DEV", "")
	t.Setenv("WINGMAN_IAP_AUDIENCE", testAudience)
	gated, err := gate(context.Background(), discardLogger(), newMux(deps))
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(gated, "/api/plans", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "opencode-go") {
		t.Fatalf("signed out: body = %q, want no plan", rec.Body.String())
	}

	key := newTestKey(t)
	signedIn := requireIAP(testValidator(t, key), testAudience, discardLogger(), newMux(deps))
	if rec := serve(signedIn, "/api/plans", sign(t, key, "ES256", iapClaims())); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "opencode-go") {
		t.Fatalf("signed in: status = %d, body = %q, want the plans", rec.Code, rec.Body.String())
	}
}
