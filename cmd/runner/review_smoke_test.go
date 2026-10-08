package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	freeModel     = "command-code/poolside/laguna-s-2.1-free"
	otherFree     = "command-code/inclusionai/ling-3.1-flash:free"
	pricedModel   = "command-code/deepseek/deepseek-v4.1-flash"
	unpricedModel = "command-code/unpriced/model"
	tokensUsed    = `{"type":"step_finish","part":{"tokens":{"input":1000,"output":100}}}` + "\n"
)

// smokeRun is what the fake review agent does for one model.
type smokeRun func(ctx context.Context, dir string, stdout, stderr io.Writer) error

// smokeAgent runs the smokeRun its bound model names, recording each directory.
type smokeAgent struct {
	model string
	runs  map[string]smokeRun
	dirs  *[]string
}

func (a smokeAgent) WithModel(model string) runner.Agent { a.model = model; return a }

func (a smokeAgent) Run(ctx context.Context, dir, _, _, _ string, stdout, stderr io.Writer) error {
	*a.dirs = append(*a.dirs, dir)
	return a.runs[a.model](ctx, dir, stdout, stderr)
}

func newSmokeAgent(runs map[string]smokeRun) (smokeAgent, *[]string) {
	dirs := &[]string{}
	return smokeAgent{runs: runs, dirs: dirs}, dirs
}

func reviewReply(events, findings string) smokeRun {
	return reply(events + `{"type":"text","part":{"text":` + strconv.Quote("```review-findings\n"+findings+"\n```") + `}}` + "\n")
}

func reply(events string) smokeRun {
	return func(_ context.Context, _ string, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, events)
		return err
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readSummary(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReviewSmokeWritesARowPerOutcome(t *testing.T) {
	agent, _ := newSmokeAgent(map[string]smokeRun{
		freeModel:      reviewReply(tokensUsed, "Percent panics on division by zero"),
		otherFree:      reviewReply(tokensUsed, ""),
		pricedModel:    reviewReply(tokensUsed, ""),
		unpricedModel:  reviewReply(tokensUsed, ""),
		"p/unreadable": reply(`{"type":"text","part":{"text":"looks fine"}}` + "\n"),
		"p/failing": func(_ context.Context, _ string, _, stderr io.Writer) error {
			_, _ = io.WriteString(stderr, "rate limited\n")
			return errors.New("exit status 1")
		},
	})
	path := filepath.Join(t.TempDir(), "summary.md")
	models := []string{freeModel, otherFree, pricedModel, unpricedModel, "p/unreadable", "p/failing"}
	if err := runReviewSmoke(context.Background(), quietLogger(), agent, models, time.Minute, path, nil); err != nil {
		t.Fatal(err)
	}
	got := readSummary(t, path)
	for _, want := range []string{
		"| model | outcome | cost | defect | detail |\n|---|---|---|---|---|\n",
		"| command-code/poolside/laguna-s-2.1-free | pass | $0 | flagged | Percent panics on division by zero |\n",
		"| command-code/inclusionai/ling-3.1-flash:free | pass | $0 | missed | no findings |\n",
		"| command-code/deepseek/deepseek-v4.1-flash | cost > $0 | $0.0004",
		"| command-code/unpriced/model | unpriced | — | missed | no rate card in providers.json |\n",
		"| p/unreadable | findings unreadable | — | — | looks fine |\n",
		"| p/failing | agent failed | — | — | review agent on p/failing: rate limited  exit status 1 |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestReviewSmokeTimesOutOneModelAndRunsTheNext(t *testing.T) {
	agent, _ := newSmokeAgent(map[string]smokeRun{
		freeModel: func(ctx context.Context, _ string, _, _ io.Writer) error {
			<-ctx.Done()
			return errors.New("signal: killed")
		},
		otherFree: reviewReply("", ""),
	})
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := runReviewSmoke(context.Background(), quietLogger(), agent, []string{freeModel, otherFree}, 50*time.Millisecond, path, nil); err != nil {
		t.Fatal(err)
	}
	got := readSummary(t, path)
	for _, want := range []string{
		"| command-code/poolside/laguna-s-2.1-free | timed out | — | — | exceeded 50ms |\n",
		"| command-code/inclusionai/ling-3.1-flash:free | pass | $0 | missed | no findings |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestReviewSmokeWritesEachRowBeforeTheNextModelRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	var seen string
	agent, _ := newSmokeAgent(map[string]smokeRun{
		freeModel: reviewReply("", ""),
		otherFree: func(ctx context.Context, dir string, stdout, stderr io.Writer) error {
			b, err := os.ReadFile(path)
			seen = string(b)
			if err != nil {
				return err
			}
			return reviewReply("", "")(ctx, dir, stdout, stderr)
		},
	})
	if err := runReviewSmoke(context.Background(), quietLogger(), agent, []string{freeModel, otherFree}, time.Minute, path, nil); err != nil {
		t.Fatal(err)
	}
	if want := "| model | outcome | cost | defect | detail |\n|---|---|---|---|---|\n" +
		"| command-code/poolside/laguna-s-2.1-free | pass | $0 | missed | no findings |\n"; seen != want {
		t.Fatalf("summary while the second model ran = %q, want %q", seen, want)
	}
}

func TestReviewSmokeFailsOnlyWhenNoModelPasses(t *testing.T) {
	var logs bytes.Buffer
	agent, _ := newSmokeAgent(map[string]smokeRun{
		pricedModel:   reviewReply(tokensUsed, ""),
		unpricedModel: reviewReply(tokensUsed, ""),
	})
	err := runReviewSmoke(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), agent,
		[]string{pricedModel, unpricedModel}, time.Minute, "", nil)
	if err == nil || !strings.Contains(logs.String(), "msg=reviewSmokeNonePassed") {
		t.Fatalf("err = %v, logs:\n%s", err, logs.String())
	}
}

func TestReviewSmokeRunsEachModelInAFreshDirectory(t *testing.T) {
	agent, dirs := newSmokeAgent(map[string]smokeRun{freeModel: reviewReply("", ""), otherFree: reviewReply("", "")})
	if err := runReviewSmoke(context.Background(), quietLogger(), agent, []string{freeModel, otherFree}, time.Minute, "", nil); err != nil {
		t.Fatal(err)
	}
	if len(*dirs) != 2 || (*dirs)[0] == (*dirs)[1] {
		t.Fatalf("directories = %q, want two distinct", *dirs)
	}
	for _, d := range *dirs {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was left behind: %v", d, err)
		}
	}
}

func TestReviewSmokeRefusesABuildFallbackInTheList(t *testing.T) {
	err := reviewSmoke(context.Background(), quietLogger(), func(string) string { return "" },
		[]string{"-review-models", otherFree, "-build-model", pricedModel})
	if err == nil || !strings.Contains(err.Error(), "fallbacks") {
		t.Fatalf("err = %v, want the build fallback refused", err)
	}
}

func TestReviewSmokeRefusesANonPositiveTimeout(t *testing.T) {
	err := reviewSmoke(context.Background(), quietLogger(), func(string) string { return "" }, []string{"-model-timeout", "0s"})
	if err == nil || !strings.Contains(err.Error(), "not positive") {
		t.Fatalf("err = %v, want a refused timeout", err)
	}
}

func TestReviewSmokeChecksAnEmptyBuildModelAgainstTheDefault(t *testing.T) {
	args := []string{"-build-model", "", "-review-models", runner.DefaultModel()}
	err := reviewSmoke(context.Background(), quietLogger(), func(string) string { return "" }, args)
	if err == nil || !strings.Contains(err.Error(), "is the build model") {
		t.Fatalf("err = %v, want the default build model refused as a review model", err)
	}
}

func TestReviewSmokeStopsAfterTheRowOfAModelRunningWhenTheJobIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent, dirs := newSmokeAgent(map[string]smokeRun{
		freeModel: func(_ context.Context, _ string, stdout, _ io.Writer) error {
			cancel()
			return reviewReply(tokensUsed, "")(ctx, "", stdout, nil)
		},
		otherFree: reviewReply(tokensUsed, ""),
	})
	summary := filepath.Join(t.TempDir(), "summary.md")
	err := runReviewSmoke(ctx, quietLogger(), agent, []string{freeModel, otherFree}, time.Minute, summary, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(*dirs) != 1 || !strings.Contains(readSummary(t, summary), "| "+freeModel+" |") {
		t.Fatalf("ran %d models, summary:\n%s", len(*dirs), readSummary(t, summary))
	}
}
