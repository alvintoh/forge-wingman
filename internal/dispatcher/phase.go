package dispatcher

import (
	"context"
	"strings"
)

// PhaseState is where a project's phase stands against the owner's pause.
type PhaseState string

const (
	PhaseRunning  PhaseState = "running"
	PhaseStopping PhaseState = "stopping"
	PhaseStopped  PhaseState = "stopped"
)

// Phase reads the owner's pause against the runs of the project in flight: a
// paused project still has runs finishing is stopping, and stopped once none is.
func Phase(paused bool, inFlight int) PhaseState {
	switch {
	case !paused:
		return PhaseRunning
	case inFlight > 0:
		return PhaseStopping
	default:
		return PhaseStopped
	}
}

// Phases reads the repositories the owner has paused, lower-cased.
type Phases interface {
	Paused(ctx context.Context) (map[string]bool, error)
}

// pausedSet is one poll's read of the paused repositories; all holds every one
// when the read failed.
type pausedSet struct {
	repos map[string]bool
	all   bool
}

func (p pausedSet) holds(repo string) bool {
	return p.all || p.repos[strings.ToLower(repo)]
}
