package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubInbox answers with the runs it holds and counts its reads.
type stubInbox struct {
	runs  []store.InboxRun
	err   error
	reads atomic.Int32
	// since and limit are what the last read asked for.
	since time.Time
	limit int
	// hold blocks a read until closed; entered is sent to as a read starts.
	hold    chan struct{}
	entered chan struct{}
}

func (s *stubInbox) Inbox(_ context.Context, since time.Time, limit int) ([]store.InboxRun, error) {
	s.reads.Add(1)
	s.since, s.limit = since, limit
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.hold != nil {
		<-s.hold
	}
	return s.runs, s.err
}

func getInbox(mux *http.ServeMux) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox", nil))
	return rec
}

func TestInboxListsEachWaitingRunWithoutTicketText(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "r1", Ticket: "ABC-1", Repo: "octo/scratch", Size: "L", PRURL: "https://github.com/octo/scratch/pull/7",
			WaitReason: runner.WaitSizeL, UpdatedAt: measuredAt()},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, AutoMerge: &stubAutoMerge{enabled: true}}))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, max-age=30" {
		t.Fatalf("status = %d, cache = %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	want := `{"runs":[{"run":"r1","ticket":"ABC-1","repo":"octo/scratch","size":"L","pr_url":"https://github.com/octo/scratch/pull/7","wait_reason":"size-l","updated_at":"2026-10-18T09:00:00Z","attempt":0,"retryable":false}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
}

// stubPulls answers PullSummaries with the summaries it holds and counts the reads.
type stubPulls struct {
	summaries map[dispatcher.PullRef]dispatcher.PullSummary
	err       error
	reads     int
	refs      []dispatcher.PullRef
}

func (s *stubPulls) PullSummaries(_ context.Context, refs []dispatcher.PullRef) (map[dispatcher.PullRef]dispatcher.PullSummary, error) {
	s.reads++
	s.refs = refs
	return s.summaries, s.err
}

func TestInboxCarriesGitHubsViewOfEachPRFromOneRead(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "r1", Repo: "octo/scratch", PRURL: "https://github.com/octo/scratch/pull/7"},
		{Run: "r2", Repo: "octo/scratch", PRURL: "https://github.com/octo/scratch/pull/8"},
		{Run: "r3", Repo: "octo/scratch", PRURL: "https://github.com/octo/scratch/pull/9"},
	}}
	pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{
		{Repo: "octo/scratch", Number: 7}: {Title: "feat: a", State: dispatcher.PullStateOpen, HeadSHA: "abc123", Checks: dispatcher.CheckGreen, ChecksSummary: "go and web"},
		{Repo: "octo/scratch", Number: 8}: {Title: "fix: b", State: dispatcher.PullStateOpen, Draft: true, HeadSHA: "def456", Checks: dispatcher.CheckFailing, ChecksSummary: "web"},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Runs) != 3 {
		t.Fatalf("body = %s, %v", rec.Body.String(), err)
	}
	if pulls.reads != 1 || len(pulls.refs) != 3 {
		t.Fatalf("GitHub read %d times for %v, want one read of all three PRs", pulls.reads, pulls.refs)
	}
	one, two, three := got.Runs[0], got.Runs[1], got.Runs[2]
	if one["title"] != "feat: a" || one["head_sha"] != "abc123" || one["draft"] != false ||
		!reflect.DeepEqual(one["checks"], map[string]any{"state": "green", "summary": "go and web"}) {
		t.Errorf("first = %v, want GitHub's title, head, draft and checks", one)
	}
	if two["draft"] != true || !reflect.DeepEqual(two["checks"], map[string]any{"state": "failing", "summary": "web"}) {
		t.Errorf("second = %v, want a failing draft", two)
	}
	for _, k := range []string{"title", "head_sha", "draft", "checks"} {
		if _, ok := three[k]; ok {
			t.Errorf("third has %q though GitHub did not return its PR: %v", k, three)
		}
	}
}

func TestInboxLeavesOutRunsWhosePRIsClosedOrMerged(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "open", PRURL: "https://github.com/octo/scratch/pull/7"},
		{Run: "closed", PRURL: "https://github.com/octo/scratch/pull/8"},
		{Run: "merged", PRURL: "https://github.com/octo/scratch/pull/9"},
		{Run: "unknown", PRURL: "https://github.com/octo/scratch/pull/11"},
		{Run: "unread", PRURL: "https://github.com/octo/scratch/pull/10"},
	}}
	pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{
		{Repo: "octo/scratch", Number: 7}:  {Title: "feat: a", State: dispatcher.PullStateOpen, HeadSHA: "abc123", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
		{Repo: "octo/scratch", Number: 8}:  {Title: "fix: b", State: dispatcher.PullStateClosed, HeadSHA: "def456", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
		{Repo: "octo/scratch", Number: 9}:  {Title: "chore: c", State: dispatcher.PullStateMerged, HeadSHA: "0a0a0a", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
		{Repo: "octo/scratch", Number: 11}: {Title: "docs: d", HeadSHA: "1b1b1b", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("status %d, body %s, %v", rec.Code, rec.Body.String(), err)
	}
	var listed []string
	for _, r := range got.Runs {
		listed = append(listed, r["run"].(string))
	}
	if !reflect.DeepEqual(listed, []string{"open", "unknown", "unread"}) {
		t.Fatalf("listed %v, want the open PR, the one whose state GitHub did not say and the one it did not return", listed)
	}
	if got.Runs[0]["title"] != "feat: a" || got.Runs[0]["head_sha"] != "abc123" {
		t.Errorf("open row = %v, want GitHub's title and head", got.Runs[0])
	}
}

func TestInboxStillListsWhenGitHubCannotBeRead(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{{Run: "r1", PRURL: "https://github.com/octo/scratch/pull/7", WaitReason: runner.WaitSizeL}}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox,
		Pulls: &stubPulls{err: errors.New("unreachable")}, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || len(got.Runs) != 1 {
		t.Fatalf("status %d, body %s, %v, want the run listed", rec.Code, rec.Body.String(), err)
	}
	if _, ok := got.Runs[0]["checks"]; ok || got.Runs[0]["wait_reason"] != "size-l" {
		t.Fatalf("row = %v, want the store's fields without checks", got.Runs[0])
	}
}

func TestInboxShowsAutoMergeOffOnlyWhereNoNamedReasonHoldsThePR(t *testing.T) {
	runs := []store.InboxRun{{Run: "named", WaitReason: runner.WaitFrontEnd}, {Run: "unnamed"}}
	for name, tt := range map[string]struct {
		switches   AutoMergeStore
		wantNamed  string
		wantUnname string
	}{
		"switch off":        {&stubAutoMerge{}, "front-end", "auto-merge-off"},
		"switch unreadable": {&stubAutoMerge{readErr: errors.New("unreachable")}, "front-end", "auto-merge-off"},
		"switch on":         {&stubAutoMerge{enabled: true}, "front-end", ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: &stubInbox{runs: runs}, AutoMerge: tt.switches}))
			var raw struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw.Runs) != 2 {
				t.Fatalf("body = %s, %v", rec.Body.String(), err)
			}
			if raw.Runs[0]["wait_reason"] != tt.wantNamed || raw.Runs[1]["wait_reason"] != tt.wantUnname {
				t.Fatalf("reasons = %v, %v, want %q, %q", raw.Runs[0]["wait_reason"], raw.Runs[1]["wait_reason"], tt.wantNamed, tt.wantUnname)
			}
		})
	}
}

func TestInboxReadsTheTrailingWindowUpToItsLimit(t *testing.T) {
	inbox := &stubInbox{}
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	if _, err := (Deps{Inbox: inbox, AutoMerge: &stubAutoMerge{enabled: true}}).readInbox(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-runner.TrailingWindow); !inbox.since.Equal(want) || inbox.limit != 100 {
		t.Fatalf("read since %v up to %d, want since %v up to 100", inbox.since, inbox.limit, want)
	}
}

func TestInboxWithNoRunsListsNone(t *testing.T) {
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: &stubInbox{}}))
	if rec.Body.String() != "{\"runs\":[]}\n" {
		t.Fatalf("body = %q, want an empty list, not null", rec.Body.String())
	}
}

func TestInboxWithoutAStoreIsUnavailable(t *testing.T) {
	if rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestInboxReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: slog.New(slog.NewTextHandler(&log, nil)),
		Inbox: &stubInbox{err: errors.New("firestore unreachable")}})
	if rec := getInbox(mux); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "inboxNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

func TestInboxReadsOnceForRequestsThatOverlap(t *testing.T) {
	inbox := &stubInbox{hold: make(chan struct{}), entered: make(chan struct{}, 8)}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			getInbox(mux)
		}()
	}
	<-inbox.entered
	time.Sleep(100 * time.Millisecond)
	close(inbox.hold)
	wg.Wait()
	if got := inbox.reads.Load(); got != 1 {
		t.Fatalf("reads = %d, want one shared by the three requests", got)
	}
}

func TestInboxShowsChecksNotGreenAboveTheReasonsThatNeverBlock(t *testing.T) {
	const pr = "https://github.com/octo/scratch/pull/7"
	ref := dispatcher.PullRef{Repo: "octo/scratch", Number: 7}
	for name, tt := range map[string]struct {
		stored  runner.WaitReason
		checks  string
		unread  bool
		enabled bool
		want    string
	}{
		"failing checks, nothing else holds it":        {"", dispatcher.CheckFailing, false, true, "checks-not-green"},
		"pending checks count as not green":            {runner.WaitSizeL, dispatcher.CheckPending, false, true, "checks-not-green"},
		"failing checks beat the switch being off":     {"", dispatcher.CheckFailing, false, false, "checks-not-green"},
		"failing checks beat a front-end hold":         {runner.WaitFrontEnd, dispatcher.CheckFailing, false, true, "checks-not-green"},
		"green checks leave the stored reason":         {runner.WaitFrontEnd, dispatcher.CheckGreen, false, true, "front-end"},
		"a draft is named before the checks":           {runner.WaitDraft, dispatcher.CheckFailing, false, true, "draft"},
		"an open decision is named before checks":      {runner.WaitUnconfirmedDecision, dispatcher.CheckFailing, false, true, "unconfirmed-decision"},
		"checks GitHub did not return are not made up": {"", "", true, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{ref: {State: dispatcher.PullStateOpen, Checks: tt.checks}}}
			if tt.unread {
				pulls.summaries = nil
			}
			inbox := &stubInbox{runs: []store.InboxRun{{Run: "r1", PRURL: pr, WaitReason: tt.stored}}}
			rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: tt.enabled}}))
			var got struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Runs) != 1 {
				t.Fatalf("body = %s, %v", rec.Body.String(), err)
			}
			if got.Runs[0]["wait_reason"] != tt.want {
				t.Fatalf("wait_reason = %v, want %q", got.Runs[0]["wait_reason"], tt.want)
			}
		})
	}
}

func TestInboxPageFitsOneBatchedGitHubRead(t *testing.T) {
	if inboxLimit > dispatcher.MaxSummaryRefs {
		t.Fatalf("inboxLimit %d exceeds the %d pull requests one GitHub read holds, so no row would show its checks", inboxLimit, dispatcher.MaxSummaryRefs)
	}
}

func TestInboxListsAStoppedRunWithItsAttemptTitleAndNoPRRead(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "stopped", Ticket: "ABC-1", Title: "Add the widget", Stopped: true, Attempt: 2, Outcome: "agent-failed", StopReason: "agent-exit", UpdatedAt: measuredAt()},
	}}
	pulls := &stubPulls{}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{}}))
	want := `{"runs":[{"run":"stopped","ticket":"ABC-1","repo":"","size":"","pr_url":"","wait_reason":"","updated_at":"2026-10-18T09:00:00Z",` +
		`"attempt":2,"outcome":"agent-failed","stop_reason":"agent-exit","stopped":true,"retryable":true,"title":"Add the widget"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s: no wait reason though the auto-merge switch is off", rec.Body.String(), want)
	}
	if len(pulls.refs) != 0 {
		t.Fatalf("GitHub was asked about %v for a run with no PR", pulls.refs)
	}
}

// A retried draft whose next attempt stopped still names the old draft: it is a stopped run, kept
// with that PR, which GitHub is not asked about.
func TestInboxListsAStoppedRunThatStillNamesAnEarlierDraft(t *testing.T) {
	const pr = "https://github.com/octo/scratch/pull/7"
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "kept", Ticket: "ABC-1", Title: "Add the widget", PRURL: pr, WaitReason: runner.WaitDraft, Stopped: true, Outcome: "stopped", StopReason: "plan-stage-failed", UpdatedAt: measuredAt()},
	}}
	pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{{Repo: "octo/scratch", Number: 7}: {State: dispatcher.PullStateOpen, Draft: true, Title: "feat: widget"}}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Runs) != 1 {
		t.Fatalf("body = %s, %v", rec.Body.String(), err)
	}
	row := got.Runs[0]
	if row["stopped"] != true || row["retryable"] != true || row["pr_url"] != pr || row["wait_reason"] != "" || row["title"] != "Add the widget" || row["draft"] != nil {
		t.Fatalf("row = %v, want stopped and retryable, still naming the draft, with the ticket's title, no wait reason and nothing of GitHub's", row)
	}
	if len(pulls.refs) != 0 {
		t.Fatalf("GitHub was asked about %v for a stopped run", pulls.refs)
	}
}

// A PR row's title is GitHub's: a ticket title the store holds for it is not served.
func TestInboxServesNoTicketTitleOnAPRRow(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "r1", Ticket: "ABC-1", Title: "The ticket's title", PRURL: "https://github.com/octo/scratch/pull/7", WaitReason: runner.WaitSizeL, UpdatedAt: measuredAt()},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, AutoMerge: &stubAutoMerge{enabled: true}}))
	if strings.Contains(rec.Body.String(), "title") {
		t.Fatalf("body = %s, want no title for a PR whose GitHub read is absent", rec.Body.String())
	}
}

// A draft is the owner's to retry and a ready PR is theirs to merge, and GitHub's reading of which it is
// outranks the reason the row recorded when the PR opened.
func TestInboxMarksAPRRetryableOnlyWhileItIsADraft(t *testing.T) {
	const pr = "https://github.com/octo/scratch/pull/7"
	ref := dispatcher.PullRef{Repo: "octo/scratch", Number: 7}
	for name, tt := range map[string]struct {
		stored  runner.WaitReason
		summary map[dispatcher.PullRef]dispatcher.PullSummary
		want    bool
	}{
		"a draft GitHub reports":                  {"", map[dispatcher.PullRef]dispatcher.PullSummary{ref: {State: dispatcher.PullStateOpen, Draft: true}}, true},
		"a draft the row said was ready":          {runner.WaitSizeL, map[dispatcher.PullRef]dispatcher.PullSummary{ref: {State: dispatcher.PullStateOpen, Draft: true}}, true},
		"a ready PR the row said was a draft":     {runner.WaitDraft, map[dispatcher.PullRef]dispatcher.PullSummary{ref: {State: dispatcher.PullStateOpen}}, false},
		"a ready PR":                              {runner.WaitSizeL, map[dispatcher.PullRef]dispatcher.PullSummary{ref: {State: dispatcher.PullStateOpen}}, false},
		"a draft the row names, GitHub unread":    {runner.WaitDraft, nil, true},
		"a ready PR the row names, GitHub unread": {runner.WaitSizeL, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			inbox := &stubInbox{runs: []store.InboxRun{{Run: "r1", PRURL: pr, WaitReason: tt.stored}}}
			rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: &stubPulls{summaries: tt.summary}, AutoMerge: &stubAutoMerge{enabled: true}}))
			var got struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Runs) != 1 {
				t.Fatalf("body = %s, %v", rec.Body.String(), err)
			}
			if got.Runs[0]["retryable"] != tt.want {
				t.Fatalf("retryable = %v, want %v", got.Runs[0]["retryable"], tt.want)
			}
		})
	}
}
