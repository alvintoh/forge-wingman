package store

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// stageStep is one step of phase whose request read input fresh and cacheRead
// from cache, wrote output and made steps requests.
func stageStep(phase runner.Phase, input, cacheRead, output int64, steps int) runner.Step {
	return runner.Step{Phase: phase, Round: 1,
		Tokens: runner.Usage{Input: input, CacheRead: cacheRead, Output: output, Steps: steps}}
}

// runOf is a settled run with the steps given.
func runOf(steps ...runner.Step) runner.Record { return runner.Record{Steps: steps} }

// shapeOfStage is the shape of stage among shapes.
func shapeOfStage(t *testing.T, shapes []StageShape, stage runner.Phase) StageShape {
	t.Helper()
	for _, s := range shapes {
		if s.Stage == stage {
			return s
		}
	}
	t.Fatalf("no shape reported for %s", stage)
	return StageShape{}
}

// buildRuns is n runs each with one build step of the request given.
func buildRuns(n int, input, cacheRead, output int64) []runner.Record {
	out := make([]runner.Record, n)
	for i := range out {
		out[i] = runOf(stageStep(runner.PhaseBuild, input, cacheRead, output, 1))
	}
	return out
}

func TestRequestShapesReportsEachStagesMedianRequestInPlanBuildReviewOrder(t *testing.T) {
	// Each kind has its own middle run: the shape is the median of every kind
	// by itself, not the one run that is the middle of any.
	recs := []runner.Record{
		runOf(stageStep(runner.PhaseBuild, 100, 9000, 700, 1), stageStep(runner.PhasePlan, 10, 20, 30, 1), stageStep(runner.PhaseReview, 5, 6, 7, 1)),
		runOf(stageStep(runner.PhaseBuild, 300, 7000, 500, 1), stageStep(runner.PhasePlan, 30, 10, 10, 1), stageStep(runner.PhaseReview, 7, 5, 5, 1)),
		runOf(stageStep(runner.PhaseBuild, 200, 8000, 600, 1), stageStep(runner.PhasePlan, 20, 30, 20, 1), stageStep(runner.PhaseReview, 6, 7, 6, 1)),
	}
	want := []StageShape{
		{Stage: runner.PhasePlan, Runs: 3, Shape: &RequestShape{Fresh: 20, Cached: 20, Output: 20}},
		{Stage: runner.PhaseBuild, Runs: 3, Shape: &RequestShape{Fresh: 200, Cached: 8000, Output: 600}},
		{Stage: runner.PhaseReview, Runs: 3, Shape: &RequestShape{Fresh: 6, Cached: 6, Output: 6}},
	}
	if got := requestShapes(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("shapes = %+v, want %+v", got, want)
	}
}

func TestARequestIsTheStageTotalOverItsStepsRoundedToAWholeToken(t *testing.T) {
	for _, tt := range []struct {
		name string
		step runner.Step
		want RequestShape
	}{
		{"a half token rounds up", stageStep(runner.PhaseBuild, 1001, 2001, 3001, 2), RequestShape{Fresh: 501, Cached: 1001, Output: 1501}},
		{"under a half rounds down", stageStep(runner.PhaseBuild, 1000, 2000, 3000, 3), RequestShape{Fresh: 333, Cached: 667, Output: 1000}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recs := []runner.Record{runOf(tt.step), runOf(tt.step), runOf(tt.step)}
			if s := shapeOfStage(t, requestShapes(recs), runner.PhaseBuild).Shape; s == nil || *s != tt.want {
				t.Fatalf("shape = %+v, want %+v", s, tt.want)
			}
		})
	}
}

func TestARequestSumsEveryRoundOfTheStageAndNoOtherPhase(t *testing.T) {
	// 150 fresh over 3 steps is 50 per request; the fix rounds' large steps are
	// another phase and the review's are another stage.
	round := runOf(
		stageStep(runner.PhaseBuild, 100, 1000, 40, 2),
		stageStep(runner.PhaseBuild, 50, 500, 20, 1),
		stageStep(runner.PhaseFix, 90000, 90000, 90000, 5),
		stageStep(runner.PhaseFixReview, 90000, 90000, 90000, 5),
		stageStep(runner.PhaseReview, 70000, 70000, 70000, 1),
	)
	got := requestShapes([]runner.Record{round, round, round})
	want := &RequestShape{Fresh: 50, Cached: 500, Output: 20}
	if s := shapeOfStage(t, got, runner.PhaseBuild).Shape; s == nil || *s != *want {
		t.Fatalf("build shape = %+v, want %+v", s, want)
	}
	if s := shapeOfStage(t, got, runner.PhaseReview).Shape; s == nil || s.Fresh != 70000 {
		t.Fatalf("review shape = %+v, want its own 70000 fresh", s)
	}
}

func TestARunWithNoStepsOfAStageIsSkippedForIt(t *testing.T) {
	zero := runOf(stageStep(runner.PhaseBuild, 99999, 99999, 99999, 0))
	recs := append(buildRuns(3, 10, 20, 30), zero, runner.Record{})
	b := shapeOfStage(t, requestShapes(recs), runner.PhaseBuild)
	if b.Runs != 3 || b.Shape == nil || *b.Shape != (RequestShape{10, 20, 30}) {
		t.Fatalf("build = %+v, want the three runs that took a step, the zero-step one skipped", b)
	}
}

func TestARunWithANegativeFigureInAStageIsSkippedForIt(t *testing.T) {
	for name, step := range map[string]runner.Step{
		"input":      stageStep(runner.PhaseBuild, -1000, 20, 30, 1),
		"cache read": stageStep(runner.PhaseBuild, 10, -2000, 30, 1),
		"output":     stageStep(runner.PhaseBuild, 10, 20, -3000, 1),
		// Netted against a good round, the stage would still read one step.
		"steps": stageStep(runner.PhaseBuild, 10, 20, 30, -1),
	} {
		t.Run(name, func(t *testing.T) {
			recs := append(buildRuns(3, 10, 20, 30), runOf(stageStep(runner.PhaseBuild, 10, 20, 30, 2), step))
			b := shapeOfStage(t, requestShapes(recs), runner.PhaseBuild)
			if b.Runs != 3 || b.Shape == nil || *b.Shape != (RequestShape{10, 20, 30}) {
				t.Fatalf("build = %+v, want the three sound runs", b)
			}
		})
	}
}

func TestAStageUnderThreeUsableRunsHasNoShapeAndItsRunCount(t *testing.T) {
	for _, tt := range []struct {
		runs      int
		wantShape bool
	}{{0, false}, {2, false}, {3, true}} {
		b := shapeOfStage(t, requestShapes(buildRuns(tt.runs, 10, 20, 30)), runner.PhaseBuild)
		if b.Runs != tt.runs || (b.Shape != nil) != tt.wantShape {
			t.Errorf("%d runs: build = %+v, want its count and a shape only from three", tt.runs, b)
		}
	}
}

func TestAStageMissingFromSomeRunsUsesOnlyTheRunsThatRecordedIt(t *testing.T) {
	recs := buildRuns(5, 10, 10, 10)
	recs[1].Steps = append(recs[1].Steps, stageStep(runner.PhasePlan, 40, 50, 60, 1))
	recs[3].Steps = append(recs[3].Steps, stageStep(runner.PhasePlan, 40, 50, 60, 1))
	got := requestShapes(recs)
	if p := shapeOfStage(t, got, runner.PhasePlan); p.Runs != 2 || p.Shape != nil {
		t.Fatalf("plan = %+v, want its two runs and no shape", p)
	}
	if b := shapeOfStage(t, got, runner.PhaseBuild); b.Runs != 5 || b.Shape == nil {
		t.Fatalf("build = %+v, want all five runs", b)
	}
}

func TestAnEvenNumberOfRunsTakesTheMeanOfTheMiddleTwoRoundedHalfUp(t *testing.T) {
	// 10 and 11 give 10.5, which is 11; 20 and 22 give exactly 21.
	recs := []runner.Record{
		runOf(stageStep(runner.PhaseBuild, 22, 1, 5, 1)),
		runOf(stageStep(runner.PhaseBuild, 10, 2, 5, 1)),
		runOf(stageStep(runner.PhaseBuild, 20, 2, 5, 1)),
		runOf(stageStep(runner.PhaseBuild, 11, 1, 5, 1)),
	}
	if s := shapeOfStage(t, requestShapes(recs), runner.PhaseBuild).Shape; s == nil || s.Fresh != 16 || s.Cached != 2 {
		t.Fatalf("shape = %+v, want fresh 16 (10,11,20,22) and cached 2 (1,1,2,2: 1.5 up)", s)
	}
}

// shapeEra is far enough ahead of every other test's settlement stamp that the
// runs these tests seed are the newest in the shared collection, which is what
// the reader lists.
var shapeEra = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

// shapeRow is one seeded run: its steps, and whether it has settled.
type shapeRow struct {
	steps   []runner.Step
	settled bool
}

// settledRow is a settled run with the steps given.
func settledRow(steps ...runner.Step) shapeRow { return shapeRow{steps: steps, settled: true} }

// seedShapeRows writes rows oldest first under ids of prefix, each settled a
// minute after the one before, and removes them when the test ends. Rows an
// interrupted earlier run left under the same prefix are removed first.
func seedShapeRows(t *testing.T, client *firestore.Client, prefix string, rows []shapeRow) {
	t.Helper()
	const maxRows = 64
	for i := range maxRows {
		_, _ = client.Collection(runsCollection).Doc(prefix + "-" + strconv.Itoa(i)).Delete(context.Background())
	}
	for i, row := range rows {
		id := prefix + "-" + strconv.Itoa(i)
		rec := runner.NewRecord(id, runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "M", Body: "Add a file."}, shapeEra)
		if row.settled {
			rec.SettledAt = shapeEra.Add(time.Duration(i+1) * time.Minute)
		}
		rec.Steps = row.steps
		seedRow(t, client, id, fields(rec))
	}
}

func TestTheStoredShapesTakeOnlyTheNewestTwentySettledRuns(t *testing.T) {
	_, client := queue(t)
	// 25 settled runs. The 5 oldest carry a request that would move every
	// median if read; the newest has no review step and the next newest no step
	// at all, so a stage's runs are counted from the 20 read, not 20 per stage.
	var rows []shapeRow
	for range 5 {
		rows = append(rows, settledRow(stageStep(runner.PhaseBuild, 5000, 5000, 5000, 1), stageStep(runner.PhaseReview, 5000, 5000, 5000, 1)))
	}
	for range 18 {
		rows = append(rows, settledRow(stageStep(runner.PhaseBuild, 10, 100, 10, 1), stageStep(runner.PhaseReview, 7, 8, 9, 1)))
	}
	rows = append(rows, settledRow(), settledRow(stageStep(runner.PhaseBuild, 10, 100, 10, 1)))
	seedShapeRows(t, client, "frg361-shape-newest", rows)

	got, err := NewRecords(client).RequestShapes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []StageShape{
		{Stage: runner.PhasePlan},
		{Stage: runner.PhaseBuild, Runs: 19, Shape: &RequestShape{Fresh: 10, Cached: 100, Output: 10}},
		{Stage: runner.PhaseReview, Runs: 18, Shape: &RequestShape{Fresh: 7, Cached: 8, Output: 9}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shapes = %+v (build %+v, review %+v), want %+v", got, got[1].Shape, got[2].Shape, want)
	}
}

func TestTheStoredShapesCarryNoShapeForAStageUnderThreeOfTheTwenty(t *testing.T) {
	_, client := queue(t)
	var rows []shapeRow
	for range 18 {
		rows = append(rows, settledRow(stageStep(runner.PhaseBuild, 10, 100, 10, 1)))
	}
	rows = append(rows, settledRow(stageStep(runner.PhaseReview, 10, 20, 30, 1)), settledRow(stageStep(runner.PhaseReview, 10, 20, 30, 1)))
	seedShapeRows(t, client, "frg361-shape-few", rows)

	got, err := NewRecords(client).RequestShapes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := shapeOfStage(t, got, runner.PhaseReview); r.Runs != 2 || r.Shape != nil {
		t.Fatalf("review = %+v, want its two runs and no shape", r)
	}
}

func TestTheStoredShapesLeaveOutRunsThatHaveNotSettled(t *testing.T) {
	_, client := queue(t)
	// An unsettled row sorts below every settled one, so it reaches the 20 read
	// only while fewer than 20 runs have settled in the whole collection: the
	// check needs the settled history empty, and is skipped when it is not.
	settled, err := client.Collection(runsCollection).Where(settledAtField, ">", time.Time{}).Limit(1).Documents(context.Background()).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(settled) > 0 {
		t.Skip("the collection holds settled runs this test did not seed")
	}
	var rows []shapeRow
	for range 3 {
		rows = append(rows, settledRow(stageStep(runner.PhaseBuild, 10, 20, 30, 1)))
	}
	// Running, and stopped before its settlement: each may already hold steps.
	rows = append(rows, shapeRow{steps: []runner.Step{stageStep(runner.PhaseBuild, 7777, 7777, 7777, 1)}},
		shapeRow{steps: []runner.Step{stageStep(runner.PhaseBuild, 8888, 8888, 8888, 1)}})
	seedShapeRows(t, client, "frg361-shape-open", rows)

	got, err := NewRecords(client).RequestShapes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b := shapeOfStage(t, got, runner.PhaseBuild); b.Runs != 3 || b.Shape == nil || *b.Shape != (RequestShape{10, 20, 30}) {
		t.Fatalf("build = %+v, want the three settled runs only", b)
	}
}

func TestTheStoredShapesFailWhenASettledRowCannotBeDecoded(t *testing.T) {
	_, client := queue(t)
	id := "frg361-shape-bad"
	_, _ = client.Collection(runsCollection).Doc(id).Delete(context.Background())
	seedRow(t, client, id, map[string]any{settledAtField: shapeEra.Add(time.Hour), stepsField: "not steps"})

	if _, err := NewRecords(client).RequestShapes(context.Background()); err == nil {
		t.Fatal("the read succeeded over a row whose steps are not a list")
	}
}
