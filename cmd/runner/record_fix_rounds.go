package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// maxFixStateBytes is the most of a fix-state.json the record step reads: the
// state's own caps keep a record it wrote far below this.
const maxFixStateBytes = 4 << 20

// fixRoundsRecorder is what the record step writes a fix-round loop's end to.
type fixRoundsRecorder interface {
	SetWaitReason(ctx context.Context, id string, reason runner.WaitReason, detail string) error
	MergeFixRounds(ctx context.Context, id string, rounds []runner.ReviewFixRound) error
}

// recordFixRounds is the trusted step that records a fix-round loop's end: it
// reads the state fix-round wrote last and the outputs of fix-round, fix-await
// and fix-push, writes why the PR waits for the owner when the loop stopped, and
// merges the rounds into the run row. The round and push jobs write nothing to
// the row. A second run of it changes nothing.
func recordFixRounds(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	in, statePath, err := parseRecordFixRounds(logger, e, args)
	if err != nil {
		return err
	}
	if statePath != "" {
		s, err := readFixState(statePath)
		if err != nil {
			return err
		}
		in.State = &s
	}
	fsc, err := recordsClient(ctx, e.project, in.RunID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	return writeFixLoop(ctx, logger, store.NewRecords(fsc), in, time.Now())
}

// parseRecordFixRounds reads the step's flags into what it records, less the
// state, whose path it returns, and refuses the identity of a job that is not
// the owner's account.
func parseRecordFixRounds(logger *slog.Logger, e env, args []string) (fixLoopRecord, string, error) {
	fs := flag.NewFlagSet("record-fix-rounds", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to merge the rounds into")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the rounds ran in")
	statePath := fs.String("state", "", "the fix-state.json the last round wrote; absent when no round ran")
	roundReason := fs.String("round-reason", "", "fix-round's reason output, empty when it did not fail")
	roundStop := fs.String("round-stop", "", "fix-round's stop output")
	roundDetail := fs.String("round-detail", "", "fix-round's detail output")
	pushReason := fs.String("push-reason", "", "fix-push's refusal reason, empty when it pushed")
	pushDetail := fs.String("push-detail", "", "fix-push's detail output")
	awaitReason := fs.String("await-reason", "", "fix-await's reason output")
	awaitDetail := fs.String("await-detail", "", "fix-await's detail output")
	if err := fs.Parse(args); err != nil {
		return fixLoopRecord{}, "", err
	}
	if *runID == "" {
		return fixLoopRecord{}, "", errors.New("record-fix-rounds needs a -run-id")
	}
	if err := e.identity.CheckAccount(); err != nil {
		return fixLoopRecord{}, "", err
	}
	return fixLoopRecord{
		RunID:     *runID,
		AttemptID: buildAttempt(logger, *attemptID, e),
		End: runner.FixLoopEnd{
			RoundReason: *roundReason, PushReason: *pushReason, AwaitReason: *awaitReason, Stop: runner.FixStop(*roundStop),
			RoundNote: *roundDetail, PushNote: *pushDetail, AwaitNote: *awaitDetail,
		},
	}, *statePath, nil
}

// readFixState reads and validates the state fix-round wrote at path.
func readFixState(path string) (runner.FixRoundsState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return runner.FixRoundsState{}, fmt.Errorf("reading the fix-round state: %w", err)
	}
	if info.Size() > maxFixStateBytes {
		return runner.FixRoundsState{}, fmt.Errorf("the fix-round state is %d bytes, over %d", info.Size(), maxFixStateBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return runner.FixRoundsState{}, fmt.Errorf("reading the fix-round state: %w", err)
	}
	return runner.ParseFixRoundsState(string(raw))
}

// fixLoopRecord is what the record step knows of a fix-round loop's end.
type fixLoopRecord struct {
	RunID     string
	AttemptID string
	// State is the state the last round wrote, nil when no round ran.
	State *runner.FixRoundsState
	End   runner.FixLoopEnd
}

// writeFixLoop writes why the PR waits, when the loop stopped, and then merges
// the state's rounds, each held to the limits a summary's are. The wait reason is
// written first so a round the check refuses still leaves the PR with its reason.
func writeFixLoop(ctx context.Context, logger *slog.Logger, rec fixRoundsRecorder, in fixLoopRecord, now time.Time) error {
	if reason, detail, ok := runner.FixRoundsWait(in.End); ok {
		if err := rec.SetWaitReason(ctx, in.RunID, reason, detail); err != nil {
			return err
		}
		logger.Info("fixRoundsWaitRecorded", "run", in.RunID, "reason", string(reason), "detail", detail)
	}
	if in.State == nil || len(in.State.Rounds) == 0 {
		return nil
	}
	for _, r := range in.State.Rounds {
		if err := runner.ValidateFixRound(in.AttemptID, r, now); err != nil {
			return fmt.Errorf("fix round %d is not one this run made: %w", r.Step.Round, err)
		}
	}
	if err := rec.MergeFixRounds(ctx, in.RunID, in.State.Rounds); err != nil {
		return err
	}
	logger.Info("fixRoundsMerged", "run", in.RunID, "rounds", len(in.State.Rounds))
	return nil
}
