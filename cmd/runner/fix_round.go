package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/storage"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// fixRound runs one fix round at a pull request's head from the state the
// previous round wrote, and writes the round's artifact under -out. It holds the
// model job's identity and never pushes: the job that reads the artifact does.
// It refuses to start when its environment holds a GitHub token, before it reads
// anything else or runs an agent.
func fixRound(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("fix-round", flag.ContinueOnError)
	branch := fs.String("branch", "", "the pull request's branch")
	head := fs.String("head", "", "the commit the branch is at, which the round commits on")
	statePath := fs.String("state", "", "the state the previous round wrote; absent on the first round")
	findings := fs.String("findings", "", "the review's findings, one per line")
	model := fs.String("model", "", "model, as provider/model")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	ticketFile := fs.String("ticket-file", "", "path to the run's ticket, as the ticket subcommand wrote it")
	out := fs.String("out", "", "directory to write the round's artifact to")
	agentImage := fs.String("agent-image", "", agentImageRequiredUsage)
	agentEgress := fs.String("agent-egress", "", agentEgressUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	round := 0
	fail := func(err error) error { return finishFixRound(logger, e.output, round, runner.FixRoundResult{}, err) }
	if err := runner.FixRoundIdentity(e.identity); err != nil {
		return fail(err)
	}
	if *out == "" {
		return fail(errors.New("fix-round needs an -out directory"))
	}
	if err := requireImages(map[string]string{"agent-image": *agentImage}); err != nil {
		return fail(err)
	}
	var state *runner.FixRoundsState
	if *statePath != "" {
		raw, err := os.ReadFile(*statePath)
		if err != nil {
			return fail(fmt.Errorf("reading the fix-round state: %w", err))
		}
		s, err := runner.ParseFixRoundsState(string(raw))
		if err != nil {
			return fail(err)
		}
		state, round = &s, s.Round
	}
	t, err := readTicket(*ticketFile)
	if err != nil {
		return fail(err)
	}
	started, err := withAgents(ctx, logger, agentSettings{Image: *agentImage, Egress: *agentEgress, Model: *model}, ".", e.harnesses,
		func(contained []runner.Harness, probe func(context.Context, string) error) error {
			gcs, err := storage.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("storage client: %w", err)
			}
			defer func() { _ = gcs.Close() }()

			logger = logger.With("attempt", e.attemptID, "ticket", t.ID)
			res, err := runner.FixRoundJob(ctx, runner.BuildDeps{
				Projections: store.NewBucket(gcs, e.project+"-projections"),
				Completions: store.NewBucket(gcs, e.project+"-completions"),
				Agent:       runner.NewRouter(runner.ProfileBuild, contained...),
				AgentProbe:  probe,
				Logger:      logger,
				Now:         time.Now,
			}, runner.BuildConfig{
				AttemptID: e.attemptID,
				Repo:      ".",
				TempDir:   e.tempDir,
				Pointer:   *pointer,
				Model:     *model,
				Secrets:   e.secrets,
				Identity:  e.identity,
				Ticket:    t,
			}, runner.FixRoundInput{Branch: *branch, Head: *head, Findings: *findings, State: state, OutDir: *out})
			return finishFixRound(logger, e.output, round, res, err)
		})
	if !started {
		return fail(err)
	}
	return err
}

// finishFixRound reports a round to the step that reads it: the commit (empty
// when none), the round the loop is at, the fix stop when the loop ended here and
// the stop reason and detail when the round failed. A loop that ended — its
// budget spent, its cap reached, a round that changed no file — returns nil;
// anything else is a failed run, logged once here.
func finishFixRound(logger *slog.Logger, output string, round int, res runner.FixRoundResult, err error) error {
	reason, detail := "", res.Detail
	if err != nil {
		reason, detail = "error", err.Error()
		var stopped *runner.StopError
		if errors.As(err, &stopped) {
			reason = string(stopped.Reason)
		}
		logger.Error("fixRoundFailed", "reason", reason, "err", err)
	} else {
		round = res.Round
		logger.Info("fixRoundDone", "round", res.Round, "commit", res.Commit, "stop", string(res.Stop))
	}
	if werr := writeOutputs(output, map[string]string{
		"commit": res.Commit,
		"round":  strconv.Itoa(round),
		"stop":   string(res.Stop),
		"reason": reason,
	}); werr != nil {
		return errors.Join(err, werr)
	}
	if werr := writeMultilineOutput(output, "detail", detail); werr != nil {
		return errors.Join(err, werr)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errRunFailed, err)
	}
	return nil
}
