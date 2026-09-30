package modelprobe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Sweep is the outcome of probing a roster.
type Sweep struct {
	Results []Result
	// Skipped are the models left unprobed because the run reached cfg.MaxModels.
	Skipped []string
}

// ErrNoEvidence marks a sweep whose every run was void, so it says nothing about any model.
var ErrNoEvidence = errors.New("no model produced a judgeable run")

// NoEvidence reports whether models were probed and every run was void, as a rejected
// credential or a dead endpoint makes it. A fail is a verdict, so it is evidence.
func (s Sweep) NoEvidence() bool {
	return len(s.Results) > 0 && !slices.ContainsFunc(s.Results, func(r Result) bool { return r.Verdict != VerdictVoid })
}

// RunSweep probes and judges up to cfg.MaxModels models, one after another, the
// incumbent first so every other model is held to its tool-call count from the
// same run. A void incumbent is probed once more before it is judged void.
func RunSweep(ctx context.Context, d Deps, incumbent string, models []string, cfg Config, logger *slog.Logger) (Sweep, error) {
	ordered := slices.Clone(models)
	if i := slices.Index(ordered, incumbent); i > 0 {
		ordered = slices.Insert(slices.Delete(ordered, i, i+1), 0, incumbent)
	}
	var sw Sweep
	if cfg.MaxModels > 0 && len(ordered) > cfg.MaxModels {
		sw.Skipped = ordered[cfg.MaxModels:]
		ordered = ordered[:cfg.MaxModels]
		logger.Warn("modelsSkipped", "kept", len(ordered), "skipped", len(sw.Skipped))
	}
	incumbentCalls := 0
	for _, model := range ordered {
		res, err := probeAndJudge(ctx, d, model, incumbentCalls, cfg, logger)
		if err != nil {
			return Sweep{}, err
		}
		if model == incumbent && res.Verdict == VerdictVoid {
			logger.Warn("incumbentReprobed", "model", model, "reason", res.Reason)
			if res, err = probeAndJudge(ctx, d, model, incumbentCalls, cfg, logger); err != nil {
				return Sweep{}, err
			}
		}
		if model == incumbent && res.Verdict == VerdictPass {
			incumbentCalls = res.ToolCalls
		}
		sw.Results = append(sw.Results, res)
	}
	return sw, nil
}

func probeAndJudge(ctx context.Context, d Deps, model string, incumbentCalls int, cfg Config, logger *slog.Logger) (Result, error) {
	obs, err := Probe(ctx, d, model, cfg)
	if err != nil {
		return Result{}, fmt.Errorf("sweeping %s: %w", model, err)
	}
	res := Judge(model, obs, incumbentCalls, cfg)
	logger.Info("modelProbed", "model", model, "verdict", string(res.Verdict), "reason", res.Reason,
		"toolCalls", res.ToolCalls, "cost", res.Usage.Cost)
	return res, nil
}

// Select narrows the free roster to only, or returns it whole when only is empty.
func Select(free, only []string) ([]string, error) {
	if len(only) == 0 {
		return free, nil
	}
	for _, m := range only {
		if runner.Provider(m) != runner.ZenProvider {
			return nil, fmt.Errorf("selecting %s: %w", m, ErrForeignProvider)
		}
		if !slices.Contains(free, m) {
			return nil, fmt.Errorf("selecting %s: not a free model on the roster", m)
		}
	}
	return only, nil
}
