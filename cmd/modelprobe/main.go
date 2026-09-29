// Command modelprobe probes the free Zen models against a fixture ticket and
// rewrites the default model set when a challenger clearly beats the incumbent.
//
//	modelprobe -repo . -models internal/runner/models.json -results probe/results
//	modelprobe -verify <results file> -models <models file> -free <json array> -summary <file>
//
// The second form checks a probe's outputs before they are proposed and renders the
// change summary from them. The first spends the free models' quota and needs OPENCODE_API_KEY in its
// environment.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/alvintoh/forge-wingman/internal/modelprobe"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

const outputFileMode = 0o644

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, os.Args[1:], time.Now)
	stop()
	if err != nil {
		logger.Error("modelprobeFailed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, args []string, now func() time.Time) error {
	cfg := modelprobe.DefaultConfig()
	fs := flag.NewFlagSet("modelprobe", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository root")
	modelsPath := fs.String("models", "internal/runner/models.json", "the default model set, rewritten when the decision changes it")
	resultsDir := fs.String("results", "probe/results", "directory the dated results file is written to")
	summary := fs.String("summary", "", "with -verify, the file to write the change summary to")
	verify := fs.String("verify", "", "results file to verify against -models and -free instead of probing")
	freeJSON := fs.String("free", "", "with -verify, the roster's free models as a JSON array")
	outputs := fs.String("outputs", "", "file to append results=, changed= and free= lines to, in GITHUB_OUTPUT form")
	runID := fs.String("run-id", "", "suffix that keeps the results file name unique; defaults to the time of day")
	bin := fs.String("opencode", "opencode", "opencode binary")
	only := fs.String("only", "", "comma-separated models to probe; skips the decision and leaves the models file alone")
	fs.Float64Var(&cfg.Margin, "margin", cfg.Margin, "ratio of the incumbent's tool calls a challenger must beat")
	fs.Float64Var(&cfg.ToolCallRatio, "threshold", cfg.ToolCallRatio, "ratio of the incumbent's tool calls above which a run fails")
	fs.IntVar(&cfg.MaxToolCalls, "max-tool-calls", cfg.MaxToolCalls, "tool calls after which a run is stopped")
	fs.IntVar(&cfg.MaxFallbacks, "max-fallbacks", cfg.MaxFallbacks, "fallbacks kept behind the default")
	fs.IntVar(&cfg.MaxModels, "max-models", cfg.MaxModels, "models probed in one run")
	fs.IntVar(&cfg.TimeoutSeconds, "timeout", cfg.TimeoutSeconds, "seconds each model gets")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	if *verify != "" {
		return verifyOutputs(*verify, *modelsPath, *freeJSON, *summary)
	}
	if !runIDPattern.MatchString(*runID) && *runID != "" {
		return fmt.Errorf("run id %q is not alphanumeric", *runID)
	}
	raw, err := os.ReadFile(*modelsPath)
	if err != nil {
		return fmt.Errorf("reading models: %w", err)
	}
	current, err := runner.ParseModelSet(raw)
	if err != nil {
		return fmt.Errorf("reading models: %w", err)
	}
	roster, err := modelprobe.ListRoster(ctx, *bin)
	if err != nil {
		return err
	}
	var onlyModels []string
	if *only != "" {
		onlyModels = strings.Split(*only, ",")
	}
	models, err := modelprobe.Select(modelprobe.FreeModels(roster), onlyModels)
	if err != nil {
		return err
	}

	script := filepath.Join(*repo, "probe", "materialize.sh")
	deps := modelprobe.Deps{
		NewAgent: func(model string) runner.Agent { return runner.Opencode{Bin: *bin, Model: model} },
		Materialize: func(ctx context.Context, dest string) error {
			if out, err := exec.CommandContext(ctx, "bash", script, dest).CombinedOutput(); err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
	sw, err := modelprobe.RunSweep(ctx, deps, current.Default, models, cfg, logger)
	if err != nil {
		return err
	}

	date := now().UTC().Format(time.DateOnly)
	var decision *modelprobe.Decision
	if len(onlyModels) == 0 {
		d := modelprobe.Decide(current, sw.Results, date, cfg)
		decision = &d
	}
	suffix := *runID
	if suffix == "" {
		suffix = now().UTC().Format("150405")
	}
	report := modelprobe.NewReport(date, cfg, sw, decision)
	resultsPath := filepath.Join(*resultsDir, date+"-"+suffix+".json")
	if err := writeReport(resultsPath, report); err != nil {
		return err
	}
	if decision != nil && decision.Changed {
		b, err := decision.Set.Marshal()
		if err != nil {
			return err
		}
		if err := os.WriteFile(*modelsPath, b, outputFileMode); err != nil {
			return fmt.Errorf("writing models: %w", err)
		}
	}
	changed := decision != nil && decision.Changed
	if *outputs != "" {
		freeList, err := json.Marshal(modelprobe.FreeModels(roster))
		if err != nil {
			return fmt.Errorf("encoding free models: %w", err)
		}
		lines := fmt.Sprintf("results=%s\nchanged=%t\nfree=%s\n", resultsPath, changed, freeList)
		if err := appendFile(*outputs, lines); err != nil {
			return err
		}
	}
	logger.Info("modelprobeDone", "changed", changed, "models", len(sw.Results), "cost", report.Usage.Cost)
	return nil
}

func writeReport(path string, r modelprobe.Report) error {
	b, err := r.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating results directory: %w", err)
	}
	if err := os.WriteFile(path, b, outputFileMode); err != nil {
		return fmt.Errorf("writing results: %w", err)
	}
	return nil
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, outputFileMode)
	if err != nil {
		return fmt.Errorf("opening outputs: %w", err)
	}
	if _, err := f.WriteString(s); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing outputs: %w", err)
	}
	return f.Close()
}

// verifyOutputs checks a probe's results file against the models file it proposes,
// then writes the change summary rendered from the results.
func verifyOutputs(resultsPath, modelsPath, freeJSON, summaryPath string) error {
	rb, err := os.ReadFile(resultsPath)
	if err != nil {
		return fmt.Errorf("reading results: %w", err)
	}
	report, err := modelprobe.ReadReport(rb)
	if err != nil {
		return err
	}
	mb, err := os.ReadFile(modelsPath)
	if err != nil {
		return fmt.Errorf("reading models: %w", err)
	}
	set, err := runner.ParseModelSet(mb)
	if err != nil {
		return err
	}
	var free []string
	if err := json.Unmarshal([]byte(freeJSON), &free); err != nil {
		return fmt.Errorf("reading free models: %w", err)
	}
	if err := modelprobe.Verify(report, set, free); err != nil {
		return fmt.Errorf("verifying outputs: %w", err)
	}
	if summaryPath == "" {
		return nil
	}
	if err := os.WriteFile(summaryPath, []byte(report.Summary()), outputFileMode); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}
