package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	recordWriteTimeout  = 30 * time.Second
	stopDetailLimit     = 2048
	logErrorLimit       = 200
	defaultAgentTimeout = 50 * time.Minute
	// bundleName is also named by run.yml's bundle verify, upload and fetch steps.
	bundleName = "wingman.bundle"
)

var modelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[A-Za-z0-9][A-Za-z0-9._:-]*)+$`)

// Agent runs the build model in a directory, streaming its events to stdout.
type Agent interface {
	Run(ctx context.Context, dir, prompt string, stdout, stderr io.Writer) error
}

// BuildDeps are the stores and the agent a build talks to.
type BuildDeps struct {
	Projections ObjectReader
	Completions ObjectCreator
	Records     RecordStore
	Agent       Agent
	Logger      *slog.Logger
	Now         func() time.Time
}

// BuildConfig identifies one build.
type BuildConfig struct {
	RecordID     string
	Repo         string
	TempDir      string
	Pointer      string
	Model        string
	AgentTimeout time.Duration
	Secret       string
	Ticket       Ticket
}

// BuildResult is what the workflow needs from a build to open the PR.
type BuildResult struct {
	Branch     string
	BundlePath string
	Changed    bool
}

// StopError ends a build with an outcome the record keeps. Build has already
// logged it by the time it is returned.
type StopError struct {
	Outcome Outcome
	Reason  StopReason
	Err     error
}

func (s *StopError) Error() string { return fmt.Sprintf("%s (%s): %v", s.Outcome, s.Reason, s.Err) }
func (s *StopError) Unwrap() error { return s.Err }

func stopWith(o Outcome, r StopReason, err error) *StopError {
	return &StopError{Outcome: o, Reason: r, Err: err}
}

// BranchName is the branch a run of ticket builds on.
func BranchName(ticketID, recordID string) string {
	return "wingman/" + ticketID + "-" + recordID
}

// Build fetches the projection, runs the agent in a fresh worktree, and commits
// and bundles what it changed.
//
// The run record is written on every return path, a panic included. The agent
// never runs unless the model is well formed and the projection was found and valid.
func Build(ctx context.Context, d BuildDeps, c BuildConfig) (res BuildResult, err error) {
	rec := newRecord(c.RecordID, c.Ticket, d.Now())
	defer func() {
		p := recover()
		var s *StopError
		switch {
		case p != nil:
			rec.Outcome, rec.StopReason = OutcomeInfraFailure, StopPanic
			rec.StopDetail = truncate(fmt.Sprint(p), stopDetailLimit)
		case errors.As(err, &s):
			rec.Outcome, rec.StopReason = s.Outcome, s.Reason
			rec.StopDetail = truncate(detail(s.Err), stopDetailLimit)
			d.Logger.Error("runStopped", "reason", string(s.Reason), "phase", string(rec.Phase),
				"err", truncate(s.Err.Error(), logErrorLimit))
		case err != nil:
			rec.Outcome = OutcomeInfraFailure
		}
		rec.BuildOutcome = rec.Outcome
		rec.UpdatedAt = d.Now()
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordWriteTimeout)
		defer cancel()
		if werr := d.Records.PutRecord(wctx, c.RecordID, rec); werr != nil {
			d.Logger.Error("recordWriteFailed", "record", c.RecordID, "err", truncate(werr.Error(), logErrorLimit))
			err = errors.Join(err, fmt.Errorf("writing record %s: %w", c.RecordID, werr))
		} else {
			d.Logger.Info("recordWritten", "record", c.RecordID, "outcome", string(rec.Outcome))
		}
		if p != nil {
			panic(p)
		}
	}()

	timed := func(p Phase, f func() error) error {
		rec.Phase = p
		d.Logger.Info("phaseStarted", "phase", string(p))
		start := d.Now()
		ferr := f()
		rec.DurationsMS[string(p)] = d.Now().Sub(start).Milliseconds()
		return ferr
	}

	var prompt string
	if err := timed(PhaseProjection, func() error {
		if !modelPattern.MatchString(c.Model) {
			return stopWith(OutcomeStopped, StopModelInvalid, errors.New("model is not provider/model"))
		}
		p, err := FetchBuildProjection(ctx, d.Projections, c.Pointer)
		var missing *MissingError
		switch {
		case errors.As(err, &missing):
			return stopWith(OutcomeStopped, StopProjectionMissing, err)
		case errors.Is(err, ErrPointerInvalid):
			return stopWith(OutcomeStopped, StopProjectionInvalid, err)
		case err != nil:
			return stopWith(OutcomeInfraFailure, StopProjectionRead, err)
		}
		rec.RuleStackSHA = p.SHA
		d.Logger.Info("projectionFetched", "sha", p.SHA)
		prompt, err = BuildPrompt(p.Text, c.Ticket)
		if err != nil {
			return stopWith(OutcomeStopped, StopProjectionInvalid, err)
		}
		return nil
	}); err != nil {
		return BuildResult{}, err
	}

	var wt Worktree
	if err := timed(PhaseWorktree, func() error {
		var err error
		wt, err = AddWorktree(ctx, c.Repo, filepath.Join(c.TempDir, "wt-"+c.RecordID), BranchName(c.Ticket.ID, c.RecordID))
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopWorktree, err)
		}
		rec.Branch = wt.Branch
		d.Logger.Info("worktreeCreated", "path", wt.Dir, "branch", wt.Branch)
		return nil
	}); err != nil {
		return BuildResult{}, err
	}

	if err := timed(PhaseBuild, func() error {
		rec.Models[string(PhaseBuild)] = c.Model
		return runAgent(ctx, d, c, wt.Dir, prompt, &rec)
	}); err != nil {
		return BuildResult{}, err
	}

	res = BuildResult{Branch: wt.Branch}
	if err := timed(PhaseCommit, func() error {
		if err := wt.Verify(ctx); err != nil {
			switch {
			case errors.Is(err, ErrGitTampered):
				return stopWith(OutcomeStopped, StopGitTampered, err)
			case errors.Is(err, ErrHeadMoved):
				return stopWith(OutcomeStopped, StopHeadMoved, err)
			}
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		files, err := wt.Commit(ctx, c.Ticket.Title+"\n\n"+c.Ticket.Body)
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		rec.EditedFiles = files
		if len(files) > 0 {
			if rec.DiffLines, err = wt.DiffLines(ctx); err != nil {
				return stopWith(OutcomeInfraFailure, StopCommit, err)
			}
		}
		d.Logger.Info("changesCommitted", "files", len(files))
		if len(files) == 0 {
			rec.Outcome = OutcomeNoChanges
			return nil
		}
		if err := wt.CheckSecret(ctx, c.Secret); err != nil {
			if errors.Is(err, ErrSecretInBranch) {
				return stopWith(OutcomeStopped, StopSecretInBranch, err)
			}
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		res.BundlePath = filepath.Join(c.TempDir, bundleName)
		if err := wt.Bundle(ctx, res.BundlePath); err != nil {
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		res.Changed = true
		rec.Outcome = OutcomeBuilt
		return nil
	}); err != nil {
		return BuildResult{}, err
	}
	return res, nil
}

// runAgent runs the agent under its own deadline with its events captured in a
// file, uploads them to the completions bucket, then totals their usage.
func runAgent(ctx context.Context, d BuildDeps, c BuildConfig, dir, prompt string, rec *Record) error {
	events := filepath.Join(c.TempDir, "completions-"+c.RecordID+".jsonl")
	stderrPath := filepath.Join(c.TempDir, "opencode-"+c.RecordID+".stderr")
	out, err := os.Create(events)
	if err != nil {
		return stopWith(OutcomeInfraFailure, StopCompletions, err)
	}
	defer func() { _ = out.Close() }()
	errOut, err := os.Create(stderrPath)
	if err != nil {
		return stopWith(OutcomeInfraFailure, StopCompletions, err)
	}
	defer func() { _ = errOut.Close() }()

	timeout := c.AgentTimeout
	if timeout <= 0 {
		timeout = defaultAgentTimeout
	}
	agentCtx, cancel := context.WithTimeout(ctx, timeout)
	runErr := d.Agent.Run(agentCtx, dir, prompt, out, errOut)
	timedOut := errors.Is(agentCtx.Err(), context.DeadlineExceeded)
	cancel()

	completions := "completions/" + c.RecordID + ".jsonl"
	for _, u := range []struct {
		name string
		f    *os.File
	}{{completions, out}, {"completions/" + c.RecordID + ".stderr.log", errOut}} {
		if _, err := u.f.Seek(0, io.SeekStart); err != nil {
			return stopWith(OutcomeInfraFailure, StopCompletions, err)
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordWriteTimeout)
		err := d.Completions.CreateObject(uctx, u.name, u.f)
		cancel()
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopCompletions, fmt.Errorf("uploading %s: %w", u.name, err))
		}
	}
	rec.CompletionsObject = completions

	if _, err := out.Seek(0, io.SeekStart); err != nil {
		rec.UsageWarning = err.Error()
	} else if usage, err := SumUsage(out); err != nil {
		rec.UsageWarning = err.Error()
	} else {
		rec.Tokens = usage
	}
	d.Logger.Info("agentFinished", "steps", rec.Tokens.Steps, "input", rec.Tokens.Input, "output", rec.Tokens.Output,
		"cacheRead", rec.Tokens.CacheRead, "cost", rec.Tokens.Cost, "usageWarning", rec.UsageWarning != "")

	switch {
	case timedOut:
		return stopWith(OutcomeAgentFailed, StopAgentTimeout, fmt.Errorf("agent exceeded %s", timeout))
	case runErr != nil:
		return stopWith(OutcomeAgentFailed, StopAgentExit, runErr)
	}
	return nil
}

func detail(err error) string {
	var g *GitError
	if errors.As(err, &g) {
		return err.Error() + ": " + g.Stderr
	}
	return err.Error()
}

// truncate caps s at n bytes on a character boundary, replacing invalid UTF-8,
// since Firestore rejects a record holding a string that is not valid UTF-8.
func truncate(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
