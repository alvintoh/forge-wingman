package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeDelegatable hands over the open issues a poll delegates from, and records
// what it delegated.
type fakeDelegatable struct {
	open    []Issue
	openErr error
	// delegateErr fails the delegation of one ticket by its id.
	delegateErr map[string]error
	opened      int
	delegated   []string
}

func (f *fakeDelegatable) Open(context.Context) ([]Issue, error) {
	f.opened++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.open, nil
}

func (f *fakeDelegatable) DelegateIssue(_ context.Context, issueID string) error {
	if err := f.delegateErr[issueID]; err != nil {
		return err
	}
	f.delegated = append(f.delegated, issueID)
	return nil
}

// openTicket is an eligible open ticket: a delegatable lane, both labels and a
// priority, in the milestone a poll starts with.
func openTicket(id string, priority int) Issue {
	return Issue{
		ID: id, URL: "https://linear.app/w/issue/" + id, Title: "[BE] add " + id,
		Body: "Add it.", Priority: priority, UUID: "uuid-" + id,
		Labels:    []string{"size:M", "repo:octo/scratch"},
		Milestone: Milestone{ID: "phase-1", SortOrder: 1},
	}
}

// delegationDeps is a poll that delegates at level from the open issues, with q
// reporting what the day has already claimed, and the log its lines land in.
func delegationDeps(level Autonomy, from []Issue, q *fakeQueue) (Deps, *fakeDelegatable, *bytes.Buffer) {
	delegatable := &fakeDelegatable{open: from}
	var logs bytes.Buffer
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Delegatable = delegatable
	deps.Autonomies = fakeAutonomies{level: level}
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	return deps, delegatable, &logs
}

// TestPollDelegatesNothingAtLevelZero is AC3: while the owner delegates by hand
// the poll reads no open issue and claims no run of its own.
func TestPollDelegatesNothingAtLevelZero(t *testing.T) {
	q := &fakeQueue{}
	deps, delegatable, logs := delegationDeps(0, []Issue{openTicket("FRG-20", 2)}, q)
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Delegated) != 0 || delegatable.opened != 0 || len(q.claimedSince) != 0 {
		t.Fatalf("delegated %v, opened %d times, counted claims %v", res.Delegated, delegatable.opened, q.claimedSince)
	}
	if strings.Contains(logs.String(), "ticketAutoDelegation") {
		t.Fatalf("level 0 logged a delegation in:\n%s", logs.String())
	}
}

// TestPollDelegatesUpToTheBarTheLevelAllows is AC1: the day's bar is the level's
// own, and a poll delegates the eligible tickets the bar and the review WIP
// limit leave room for.
func TestPollDelegatesUpToTheBarTheLevelAllows(t *testing.T) {
	three := []Issue{openTicket("FRG-20", 1), openTicket("FRG-21", 2), openTicket("FRG-22", 3)}
	for name, tt := range map[string]struct {
		level   Autonomy
		claimed int
		openPRs int
		want    []string
	}{
		"level 1":         {level: 1, want: []string{"FRG-20"}},
		"level 2":         {level: 2, want: []string{"FRG-20", "FRG-21", "FRG-22"}},
		"level 3":         {level: 3, want: []string{"FRG-20", "FRG-21", "FRG-22"}},
		"level 4":         {level: 4, want: []string{"FRG-20", "FRG-21", "FRG-22"}},
		"level 5":         {level: 5, want: []string{"FRG-20", "FRG-21", "FRG-22"}},
		"level 5 spent":   {level: 5, claimed: 9, want: []string{"FRG-20"}},
		"level 1 spent":   {level: 1, claimed: 1},
		"wip leaves room": {level: 5, openPRs: 2, want: []string{"FRG-20"}},
		"wip full":        {level: 5, openPRs: 3},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{claimedCount: tt.claimed}
			deps, delegatable, logs := delegationDeps(tt.level, three, q)
			deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/scratch": tt.openPRs}}
			res, err := Poll(context.Background(), deps, buildConfig)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(delegatable.delegated, tt.want) || !slices.Equal(res.Delegated, tt.want) {
				t.Fatalf("delegated %v (result %v), want %v:\n%s", delegatable.delegated, res.Delegated, tt.want, logs.String())
			}
		})
	}
}

// TestPollCountsTodaysClaimsFromTheStartOfTheDay is AC1: the bar is spent by the
// runs claimed today, in the UTC day the budget's own windows share.
func TestPollCountsTodaysClaimsFromTheStartOfTheDay(t *testing.T) {
	q := &fakeQueue{}
	deps, _, _ := delegationDeps(3, []Issue{openTicket("FRG-20", 1)}, q)
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	want := time.Date(pollAt.Year(), pollAt.Month(), pollAt.Day(), 0, 0, 0, 0, time.UTC)
	if len(q.claimedSince) != 1 || !q.claimedSince[0].Equal(want) {
		t.Fatalf("counted claims since %v, want %v", q.claimedSince, want)
	}
}

// TestPollLogsThatTheBarIsReachedOncePerPoll is AC4: a poll that delegates
// nothing because the day's bar is spent says so once.
func TestPollLogsThatTheBarIsReachedOncePerPoll(t *testing.T) {
	q := &fakeQueue{claimedCount: 3}
	deps, delegatable, logs := delegationDeps(3, []Issue{openTicket("FRG-20", 1)}, q)
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Delegated) != 0 || delegatable.opened != 0 {
		t.Fatalf("delegated %v, opened the open issues %d times", res.Delegated, delegatable.opened)
	}
	if n := strings.Count(logs.String(), "msg=ticketAutoDelegationSkipped"); n != 1 {
		t.Fatalf("logged %d delegation skips, want one:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "reason=bar-reached") {
		t.Fatalf("the skip does not name the bar:\n%s", logs.String())
	}
}

// TestPollDelegatesNothingWhenTheReviewWIPCannotBeRead is AC1's other half: the
// phase adds no load it cannot measure, so an unreadable count delegates nothing
// rather than guessing.
func TestPollDelegatesNothingWhenTheReviewWIPCannotBeRead(t *testing.T) {
	deps, delegatable, logs := delegationDeps(3, []Issue{openTicket("FRG-20", 1)}, &fakeQueue{})
	deps.OpenPRs = fakeOpenPRs{err: errFirestore}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(delegatable.delegated) != 0 || delegatable.opened != 0 {
		t.Fatalf("delegated %v, opened %d times", delegatable.delegated, delegatable.opened)
	}
	if !strings.Contains(logs.String(), "reason=wip-unknown") {
		t.Fatalf("the skip does not name the failed count:\n%s", logs.String())
	}
}

// TestPollLogsThatTheReviewWIPIsFull is AC1's remaining reason, and the one an
// operator reads to know the queue is waiting on review rather than on tickets.
func TestPollLogsThatTheReviewWIPIsFull(t *testing.T) {
	deps, delegatable, logs := delegationDeps(3, []Issue{openTicket("FRG-20", 1)}, &fakeQueue{})
	deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/scratch": DefaultLimits.ReviewWIP}}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(delegatable.delegated) != 0 || delegatable.opened != 0 {
		t.Fatalf("delegated %v, opened %d times", delegatable.delegated, delegatable.opened)
	}
	if !strings.Contains(logs.String(), "reason=wip-full") {
		t.Fatalf("the skip does not name the review limit:\n%s", logs.String())
	}
}

// TestPollDelegatesOnlyTicketsBreakingNoEligibilityRule is AC2: every rule but
// the milestone one is a property of the ticket itself, and a ticket breaking
// any of them is never delegated.
func TestPollDelegatesOnlyTicketsBreakingNoEligibilityRule(t *testing.T) {
	for name, breakRule := range map[string]func(*Issue){
		"no lane":           func(i *Issue) { i.Title = "Add the widget" },
		"wrong lane":        func(i *Issue) { i.Title = "[DOC] Add the widget" },
		"no size":           func(i *Issue) { i.Labels = []string{"repo:octo/scratch"} },
		"bad size":          func(i *Issue) { i.Labels = []string{"size:XL", "repo:octo/scratch"} },
		"no repo":           func(i *Issue) { i.Labels = []string{"size:M"} },
		"off allowlist":     func(i *Issue) { i.Labels = []string{"size:M", "repo:octo/other"} },
		"workflow path":     func(i *Issue) { i.Body = "Edit .github/workflows/run.yml to add a step." },
		"hold label":        func(i *Issue) { i.Labels = append(i.Labels, "wingman:hold") },
		"bad priority":      func(i *Issue) { i.Priority = 5 },
		"already delegated": func(i *Issue) { i.DelegatedTo = "agent-1" },
	} {
		t.Run(name, func(t *testing.T) {
			issue := openTicket("FRG-20", 2)
			breakRule(&issue)
			deps, delegatable, logs := delegationDeps(1, []Issue{issue}, &fakeQueue{})
			res, err := Poll(context.Background(), deps, buildConfig)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Delegated) != 0 || len(delegatable.delegated) != 0 {
				t.Fatalf("delegated %v, want a ticket breaking a rule left alone", res.Delegated)
			}
			if !strings.Contains(logs.String(), "reason=no-eligible-ticket") {
				t.Fatalf("the skip does not name the empty milestone:\n%s", logs.String())
			}
		})
	}
}

// TestPollDelegatesTheTicketsAnEligibilityRuleAllows is AC2's other direction,
// so the rule table above cannot pass by delegating nothing at all.
func TestPollDelegatesTheTicketsAnEligibilityRuleAllows(t *testing.T) {
	for name, allow := range map[string]func(*Issue){
		"an FE lane":       func(i *Issue) { i.Title = "[FE] add a screen" },
		"a SLICE lane":     func(i *Issue) { i.Title = "[SLICE] add a stage" },
		"a lowercase lane": func(i *Issue) { i.Title = "[be] add it" },
		"no priority":      func(i *Issue) { i.Priority = 0 },
		"an L size":        func(i *Issue) { i.Labels = []string{"size:L", "repo:octo/scratch"} },
		"no milestone":     func(i *Issue) { i.Milestone = Milestone{} },
		"an open blocker":  func(i *Issue) { i.BlockedByOpen = []string{"FRG-19"} },
	} {
		t.Run(name, func(t *testing.T) {
			issue := openTicket("FRG-20", 2)
			allow(&issue)
			deps, delegatable, logs := delegationDeps(1, []Issue{issue}, &fakeQueue{})
			if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(delegatable.delegated, []string{"FRG-20"}) {
				t.Fatalf("delegated %v, want FRG-20:\n%s", delegatable.delegated, logs.String())
			}
		})
	}
}

// TestPollDoesNotDelegateATicketWhoseRunHasEnded is AC2's settled-row rule: a
// run that ended leaves its ticket to the owner's own `runner requeue`.
func TestPollDoesNotDelegateATicketWhoseRunHasEnded(t *testing.T) {
	q := &fakeQueue{requeues: map[string]bool{"FRG-20": true}}
	deps, delegatable, _ := delegationDeps(1, []Issue{openTicket("FRG-20", 2)}, q)
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(delegatable.delegated) != 0 {
		t.Fatalf("delegated %v, want a ticket whose run has ended left to a requeue", delegatable.delegated)
	}
}

// TestPollDelegatesFromTheEarliestMilestoneWithOpenTickets is AC2's ordering
// rule: the milestone being worked through is finished before the next begins,
// and a ticket already delegated still counts as the milestone being worked on.
func TestPollDelegatesFromTheEarliestMilestoneWithOpenTickets(t *testing.T) {
	inFlight := openTicket("FRG-20", 2)
	inFlight.DelegatedTo = "agent-1"
	later := openTicket("FRG-21", 1)
	later.Milestone = Milestone{ID: "phase-2", SortOrder: 2}
	noMilestone := Issue{
		ID: "FRG-23", Title: "[BE] add it", Body: "Add it.", UUID: "uuid-23",
		Labels: []string{"size:M", "repo:octo/scratch"},
	}
	for name, tt := range map[string]struct {
		open []Issue
		want []string
	}{
		"the next milestone waits":  {open: []Issue{inFlight, later}},
		"the earliest is delegated": {open: []Issue{openTicket("FRG-22", 2), later}, want: []string{"FRG-22"}},
		"no milestone at all":       {open: []Issue{noMilestone}, want: []string{"FRG-23"}},
	} {
		t.Run(name, func(t *testing.T) {
			deps, delegatable, logs := delegationDeps(3, tt.open, &fakeQueue{})
			if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(delegatable.delegated, tt.want) {
				t.Fatalf("delegated %v, want %v:\n%s", delegatable.delegated, tt.want, logs.String())
			}
		})
	}
}

// TestPollDelegatesTheHighestPriorityTicketFirst is AC2's ordering within the
// milestone: Linear priority, with no priority last and a tie broken by the
// identifier, so the choice does not move between polls.
func TestPollDelegatesTheHighestPriorityTicketFirst(t *testing.T) {
	open := []Issue{
		openTicket("FRG-22", 3), openTicket("FRG-23", 0), openTicket("FRG-21", 1), openTicket("FRG-20", 1),
	}
	deps, delegatable, _ := delegationDeps(1, open, &fakeQueue{})
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(delegatable.delegated, []string{"FRG-20"}) {
		t.Fatalf("delegated %v, want the urgent ticket and the identifier tie-break", delegatable.delegated)
	}
}

// TestPollLogsTheRuleThatChoseTheTicketItDelegated is AC4: each delegation names
// the ticket, the level and the rule that chose it.
func TestPollLogsTheRuleThatChoseTheTicketItDelegated(t *testing.T) {
	deps, _, logs := delegationDeps(2, []Issue{openTicket("FRG-20", 1)}, &fakeQueue{})
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	want := "msg=ticketAutoDelegated ticket=FRG-20 level=2 rule=" + delegateRule
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("log does not carry %q:\n%s", want, logs.String())
	}
}

// TestPollReportsTheDelegationLinearRefused: a delegation that did not land is an
// error the poll reports, and the tickets after it are left for a later poll.
func TestPollReportsTheDelegationLinearRefused(t *testing.T) {
	open := []Issue{openTicket("FRG-20", 1), openTicket("FRG-21", 2)}
	deps, delegatable, _ := delegationDeps(3, open, &fakeQueue{})
	delegatable.delegateErr = map[string]error{"FRG-20": errors.New("the Linear API refused the mutation")}
	_, err := Poll(context.Background(), deps, buildConfig)
	if err == nil || !strings.Contains(err.Error(), "refused the mutation") {
		t.Fatalf("err = %v, want Linear's own failure", err)
	}
	if len(delegatable.delegated) != 0 {
		t.Fatalf("delegated %v, want nothing after the refusal", delegatable.delegated)
	}
}

// TestPollReportsOpenIssuesItCannotRead: the open read failing is the poll's own
// failure, as a failed delegated read is.
func TestPollReportsOpenIssuesItCannotRead(t *testing.T) {
	deps, delegatable, _ := delegationDeps(1, nil, &fakeQueue{})
	delegatable.openErr = errFirestore
	if _, err := Poll(context.Background(), deps, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the open read's failure", err)
	}
}
