package dispatcher

import "testing"

func TestPhaseReadsThePauseAgainstTheRunsInFlight(t *testing.T) {
	for name, tt := range map[string]struct {
		paused   bool
		inFlight int
		want     PhaseState
	}{
		"not paused, nothing in flight": {false, 0, PhaseRunning},
		"not paused, a run in flight":   {false, 2, PhaseRunning},
		"paused, a run in flight":       {true, 1, PhaseStopping},
		"paused, nothing in flight":     {true, 0, PhaseStopped},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Phase(tt.paused, tt.inFlight); got != tt.want {
				t.Fatalf("Phase(%v, %d) = %q, want %q", tt.paused, tt.inFlight, got, tt.want)
			}
		})
	}
}
