package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestExitCode(t *testing.T) {
	stopped := &runner.StopError{Outcome: runner.OutcomeStopped, Reason: runner.StopProjectionMissing, Err: errors.New("x")}
	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"a reported stop", stopped, 0},
		{"a stop whose summary was not reported", errors.Join(stopped, runner.ErrSummaryUnreported), 1},
		{"a stop a stage reports as a failed run", fmt.Errorf("%w: %w", errRunFailed, stopped), 1},
		{"an error that is not a stop", errors.New("boom"), 1},
		{"a recorded run that failed", errRunFailed, 1},
	} {
		if got := exitCode(tt.err); got != tt.want {
			t.Errorf("%s: exit %d, want %d", tt.name, got, tt.want)
		}
	}
}
