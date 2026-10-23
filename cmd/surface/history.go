package main

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// historyLimit is how many of the latest settled runs the history lists.
const historyLimit = 20

// RunHistoryReader lists the latest settled runs, newest first. It has no write
// method: the history only reads what the runs already recorded.
type RunHistoryReader interface {
	RecentSettled(ctx context.Context, limit int) ([]store.SettledRun, error)
}

// runHistory serves GET /api/run-history.
func (d Deps) runHistory(w http.ResponseWriter, r *http.Request) {
	if d.History == nil {
		http.Error(w, "the run history needs a run store", http.StatusServiceUnavailable)
		return
	}
	runs, err := d.History.RecentSettled(r.Context(), historyLimit)
	if err != nil {
		d.Logger.Error("runHistoryNotRead", "err", err.Error())
		http.Error(w, "reading the run history", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, renderRunHistory(runs))
}

// The kinds a settled run is drawn as. The page maps each to a colour and the
// words say what the colour alone cannot, since two kinds can share one.
const (
	kindAllowance  = "allowance"
	kindCancelled  = "cancelled"
	kindCredential = "credential"
	kindDecision   = "decision"
	kindDraft      = "draft"
	kindFailed     = "failed"
	kindMerged     = "merged"
	kindNoChanges  = "no-changes"
	kindOther      = "other"
	kindPROpen     = "pr-open"
)

// historyResponse is the run history endpoint's wire shape: the settled runs
// oldest first, as the strip draws them, and how many of them were merged. It
// carries no ticket text, for the reason the comparison does not.
type historyResponse struct {
	Runs   []historyRun `json:"runs"`
	Merged int          `json:"merged"`
}

type historyRun struct {
	Run       string    `json:"run"`
	Ticket    string    `json:"ticket"`
	Kind      string    `json:"kind"`
	Words     string    `json:"words"`
	SettledAt time.Time `json:"settled_at"`
}

// renderRunHistory renders the store's newest-first runs oldest first, each
// with the kind and the words for how it ended. The merged count comes from the
// same rows as the runs, so the two cannot disagree.
func renderRunHistory(runs []store.SettledRun) historyResponse {
	out := historyResponse{Runs: make([]historyRun, 0, len(runs))}
	for _, run := range slices.Backward(runs) {
		kind, words := runKind(run)
		if kind == kindMerged {
			out.Merged++
		}
		out.Runs = append(out.Runs, historyRun{
			Run: run.Run, Ticket: run.Ticket, Kind: kind, Words: words, SettledAt: run.SettledAt,
		})
	}
	return out
}

// runKind is how a settled run ended, in a kind the page colours and the words
// it is read as. Every run lands in some kind: one the surface cannot place is
// kindOther rather than a failure it cannot vouch for.
func runKind(run store.SettledRun) (kind, words string) {
	if run.Merged {
		return kindMerged, "merged"
	}
	if run.Cancelled {
		return kindCancelled, "cancelled by you"
	}
	if run.Outcome == runner.OutcomePROpened {
		if run.WaitReason == runner.WaitUnconfirmedDecision {
			return kindDecision, "stopped at an unconfirmed decision"
		}
		if run.WaitReason == runner.WaitDraft {
			return kindDraft, "draft PR with open boxes"
		}
		return kindPROpen, "PR open, waiting on you"
	}
	switch {
	case run.Outcome == runner.OutcomeBudgetStop || run.StopReason == runner.StopAllowanceExhausted:
		return kindAllowance, "stopped, allowance spent"
	case run.StopReason == runner.StopCredentialAbsent:
		return kindCredential, "stopped, credential missing"
	case run.StopReason == runner.StopWaitingOnOwner:
		return kindDecision, "stopped at an unconfirmed decision"
	case run.Outcome == runner.OutcomeNoChanges:
		return kindNoChanges, "ended with no changes"
	case run.Outcome == runner.OutcomeStopped, run.Outcome == runner.OutcomeAgentFailed, run.Outcome == runner.OutcomeInfraFailure:
		return kindFailed, "failed"
	}
	return kindOther, "ended without a PR"
}
