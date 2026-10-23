package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// InboxStore lists the runs whose PR waits on the owner, newest first.
type InboxStore interface {
	Inbox(ctx context.Context, since time.Time, limit int) ([]store.InboxRun, error)
}

// PullLister reads title, draft, head and checks of many pull requests in one
// call, for the inbox to show.
type PullLister interface {
	PullSummaries(ctx context.Context, refs []dispatcher.PullRef) (map[dispatcher.PullRef]dispatcher.PullSummary, error)
}

// inbox serves GET /api/inbox, sharing one read among the requests inboxFlight
// holds in flight.
func (d Deps) inbox(inboxFlight *singleflight.Group) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Inbox == nil {
			http.Error(w, "the inbox needs a run store", http.StatusServiceUnavailable)
			return
		}
		view, err, _ := inboxFlight.Do("inbox", func() (any, error) {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), inboxReadTimeout)
			defer cancel()
			return d.readInbox(ctx, time.Now())
		})
		if err != nil {
			d.Logger.Error("inboxNotRead", "err", err.Error())
			http.Error(w, "reading the inbox", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(inboxMaxAgeSeconds))
		writeJSON(w, http.StatusOK, view)
	}
}

const (
	inboxLimit         = 100
	inboxMaxAgeSeconds = 30
	inboxReadTimeout   = 10 * time.Second
	// mergeTimeout bounds one merge request across its GitHub calls and its writes.
	mergeTimeout = 30 * time.Second
)

// inboxResponse is the inbox endpoint's wire shape. It carries no ticket text,
// for the reason the comparison does not; the PR title is GitHub's, not the ticket's.
// A run that stopped with no PR is listed too, with an empty pr_url, no title and
// the reason it stopped for the page to word.
type inboxResponse struct {
	Runs []inboxRun `json:"runs"`
}

type inboxRun struct {
	Run    string `json:"run"`
	Ticket string `json:"ticket"`
	Repo   string `json:"repo"`
	Size   string `json:"size"`
	PRURL  string `json:"pr_url"`
	// WaitReason is why the PR waits for the owner, empty for a PR nothing but a
	// switch or a failed gate holds.
	WaitReason string    `json:"wait_reason"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Attempt is the attempt the row is on, from 0, which a retry sends back to say
	// which it saw. Outcome and StopReason are how that attempt ended, and Retryable
	// whether the owner may retry it: a stopped run, or one whose PR is a draft.
	Attempt    int    `json:"attempt"`
	Outcome    string `json:"outcome,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	Retryable  bool   `json:"retryable"`
	// The rest is GitHub's view of the PR as of the cached read, and is absent
	// where the read failed or the surface holds no merge token. The merge route
	// never trusts it: it reads the PR again.
	Title   string       `json:"title,omitempty"`
	HeadSHA string       `json:"head_sha,omitempty"`
	Draft   *bool        `json:"draft,omitempty"`
	Checks  *inboxChecks `json:"checks,omitempty"`
}

// inboxChecks is how a PR's head checks stand: green, failing or pending, and
// the checks that say so.
type inboxChecks struct {
	State   string `json:"state"`
	Summary string `json:"summary"`
}

// readInbox lists the runs waiting on the owner. A run whose PR GitHub reports
// closed or merged is left out, since the run's record can lag a PR closed or
// merged outside the surface; one whose PR was not read stays. A PR whose checks
// are not green shows checks-not-green unless a draft or an unconfirmed decision,
// which block the merge first, names it; a run whose PR no reason holds shows
// auto-merge-off while the switch is off, so a change to the switch shows
// without a new run.
func (d Deps) readInbox(ctx context.Context, now time.Time) (inboxResponse, error) {
	runs, err := d.Inbox.Inbox(ctx, now.Add(-runner.TrailingWindow), inboxLimit)
	if err != nil {
		return inboxResponse{}, err
	}
	switchOn, _ := d.autoMergeSwitch(ctx)
	pulls := d.pullSummaries(ctx, runs)
	out := inboxResponse{Runs: make([]inboxRun, 0, len(runs))}
	for _, r := range runs {
		reason := r.WaitReason
		row := inboxRun{Run: r.Run, Ticket: r.Ticket, Repo: r.Repo, Size: r.Size,
			PRURL: r.PRURL, UpdatedAt: r.UpdatedAt, Attempt: r.Attempt, Outcome: r.Outcome, StopReason: r.StopReason}
		if r.Stopped {
			// A stopped run waits for a retry, not for a merge, so no switch or check holds it.
			row.Retryable = true
			out.Runs = append(out.Runs, row)
			continue
		}
		// GitHub's reading decides whether the PR is still a draft; the row's reason
		// stands in for it where the read failed.
		row.Retryable = r.WaitReason == runner.WaitDraft
		if repo, number, err := parsePullURL(r.PRURL); err == nil {
			if p, ok := pulls[dispatcher.PullRef{Repo: repo, Number: number}]; ok {
				if p.State == dispatcher.PullStateClosed || p.State == dispatcher.PullStateMerged {
					continue
				}
				row.Title, row.HeadSHA, row.Draft = p.Title, p.HeadSHA, &p.Draft
				row.Retryable = p.Draft
				row.Checks = &inboxChecks{State: p.Checks, Summary: p.ChecksSummary}
				if p.Checks != dispatcher.CheckGreen && reason != runner.WaitUnconfirmedDecision && reason != runner.WaitDraft {
					reason = runner.WaitChecksNotGreen
				}
			}
		}
		if reason == "" && !switchOn {
			reason = runner.WaitAutoMergeOff
		}
		row.WaitReason = string(reason)
		out.Runs = append(out.Runs, row)
	}
	return out, nil
}

// pullSummaries reads the listed PRs from GitHub in one request. A list the
// surface cannot enrich is still a list, so a failed read is logged and the
// rows go without GitHub's fields.
func (d Deps) pullSummaries(ctx context.Context, runs []store.InboxRun) map[dispatcher.PullRef]dispatcher.PullSummary {
	if d.Pulls == nil {
		return nil
	}
	var refs []dispatcher.PullRef
	for _, r := range runs {
		if repo, number, err := parsePullURL(r.PRURL); err == nil {
			refs = append(refs, dispatcher.PullRef{Repo: repo, Number: number})
		}
	}
	summaries, err := d.Pulls.PullSummaries(ctx, refs)
	if err != nil {
		d.Logger.Error("inboxPullsNotRead", "err", err.Error())
		return nil
	}
	return summaries
}
