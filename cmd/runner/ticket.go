package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// ticket writes the run record's ticket to the file -out names — carrying the
// plan stage's file list when one is recorded, so the build skips planning —
// and fails when the identity does not match, the record is missing or its
// ticket cannot be built.
func ticket(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	out := fs.String("out", "", "path to write the run's ticket to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	records := store.NewRecords(fsc)
	rec, err := runRecord(ctx, logger, records, *runID)
	if err != nil {
		return err
	}
	t := rec.Ticket()
	logger.Info("ticketRead", "run", *runID, "ticket", t.ID)
	if rec.LastResort && rec.LastResortModels.Build == "" {
		return fmt.Errorf("run %s was claimed on a free tier but names no free model", *runID)
	}
	plan, err := records.GetPlan(ctx, *runID)
	if err != nil {
		return err
	}
	t.PlanFiles = plan.Files
	answered, err := store.NewQueue(fsc).Decisions(ctx, *runID)
	if err != nil {
		return err
	}
	t.Decisions = answered
	return writeTicket(*out, e.output, t, rec.RunModels())
}

// writeTicket writes the ticket to path and the models it named to $GITHUB_OUTPUT.
// The ticket never travels as a job output: GitHub drops any output holding a
// value masked in this job, which is how a run reaches the model job empty, so
// only the model labels — never ticket text — are outputs.
func writeTicket(path, output string, t runner.Ticket, m runner.ModelLabels) error {
	if path == "" {
		return errors.New("no -out path to write the ticket to")
	}
	v, err := t.Encode()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
		return fmt.Errorf("writing the ticket: %w", err)
	}
	return writeOutputs(output, modelOutputs(m))
}

// modelOutputs are the models the ticket named, each empty when it named none,
// so a run.yml expression falls back to its own default.
func modelOutputs(m runner.ModelLabels) map[string]string {
	return map[string]string{
		"override_model":         m.Build,
		"override_review_models": m.Review,
		"override_plan_models":   strings.Join(m.Plan, ","),
	}
}

// errTicketNotDelivered reports no ticket file where the build expected one: the
// hand-off a masked job output silently dropped, or an artifact that never came.
var errTicketNotDelivered = errors.New("ticket not delivered")

// readTicket reads the ticket the ticket job wrote to path. An absent or empty
// file is errTicketNotDelivered, a hand-off failure distinct from a file that is
// present but does not decode, which is runner.ErrTicketInvalid.
func readTicket(path string) (runner.Ticket, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return runner.Ticket{}, fmt.Errorf("%w: %w", errTicketNotDelivered, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return runner.Ticket{}, fmt.Errorf("%w: %s is empty", errTicketNotDelivered, path)
	}
	return runner.ParseTicket(string(raw))
}
