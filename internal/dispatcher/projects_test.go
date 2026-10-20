package dispatcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

var phaseNow = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

type fakePhaseProjects struct {
	listed   []PhaseProject
	resolved []string
}

func (f *fakePhaseProjects) Listed(context.Context) ([]PhaseProject, error) { return f.listed, nil }

func (f *fakePhaseProjects) ResolveStart(_ context.Context, repo string, _ time.Time, refusal string) error {
	f.resolved = append(f.resolved, repo+":"+refusal)
	return nil
}

type fakeSnapshots struct{ got []PhaseSnapshot }

func (f *fakeSnapshots) RecordSnapshots(_ context.Context, s []PhaseSnapshot) error {
	f.got = s
	return nil
}

type fakeRuns map[string]TicketRun

func (f fakeRuns) TicketRuns(context.Context, []string) (map[string]TicketRun, error) { return f, nil }

type fakeProjectIssues struct {
	issues    []Issue
	err       error
	delegated []string
	reads     int
}

func (f *fakeProjectIssues) ProjectIssues(context.Context, []string) ([]Issue, error) {
	f.reads++
	return f.issues, f.err
}

func (f *fakeProjectIssues) DelegateIssue(_ context.Context, id string) error {
	f.delegated = append(f.delegated, id)
	return nil
}

func phaseIssue(id, state, milestone string, order float64, delegate string) Issue {
	return Issue{
		ID: id, UUID: "uuid-" + id, Title: "[BE] " + id, Labels: []string{"size:S", "repo:octo/widget"},
		StateType: state, DelegatedTo: delegate, Project: "Roadmap",
		Milestone: Milestone{ID: milestone, Name: milestone, SortOrder: order},
	}
}

func phaseDeps(projects *fakePhaseProjects, snaps *fakeSnapshots, runs fakeRuns, issues *fakeProjectIssues) Deps {
	return Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return phaseNow },
		ProjectPhases: ProjectPhaseDeps{
			Projects: projects, Snapshots: snaps, Runs: runs, Issues: issues,
		},
	}
}

func widget(phase string) PhaseProject {
	return PhaseProject{Repository: "octo/widget", LinearProject: "Roadmap", StartPhase: phase, StartRequestedAt: phaseNow}
}

func TestPhaseEligibleTable(t *testing.T) {
	ok := phaseIssue("FRG-1", "unstarted", "P1", 1, "")
	with := func(f func(*Issue)) Issue {
		i := ok
		f(&i)
		return i
	}
	tests := []struct {
		name  string
		issue Issue
		run   TicketRun
		want  bool
	}{
		{"code lane with a size", ok, TicketRun{}, true},
		{"slice lane in any case", with(func(i *Issue) { i.Title = "[slice] x" }), TicketRun{}, true},
		{"infra lane", with(func(i *Issue) { i.Title = "[INFRA] x" }), TicketRun{}, false},
		{"ops lane", with(func(i *Issue) { i.Title = "[OPS] x" }), TicketRun{}, false},
		{"no size label", with(func(i *Issue) { i.Labels = []string{"repo:octo/widget"} }), TicketRun{}, false},
		{"another repository", with(func(i *Issue) { i.Labels = []string{"size:S", "repo:octo/other"} }), TicketRun{}, false},
		{"started", with(func(i *Issue) { i.StateType = "started" }), TicketRun{}, false},
		{"completed", with(func(i *Issue) { i.StateType = "completed" }), TicketRun{}, false},
		{"names a workflow path", with(func(i *Issue) { i.Body = "edit .github/workflows/ci.yml" }), TicketRun{}, false},
		{"held by the owner", with(func(i *Issue) { i.Labels = append([]string{"Wingman:Hold"}, i.Labels...) }), TicketRun{}, false},
		{"already delegated", with(func(i *Issue) { i.DelegatedTo = "agent-1" }), TicketRun{}, false},
		{"run row ended", ok, TicketRun{Ended: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := phaseEligible(tt.issue, "octo/widget", tt.run); got != tt.want {
				t.Errorf("phaseEligible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPollProjectsSnapshotsTheCurrentPhase(t *testing.T) {
	snaps := &fakeSnapshots{}
	blocked := phaseIssue("FRG-4", "unstarted", "P2", 2, "")
	blocked.BlockedByOpen = []string{"FRG-3"}
	issues := &fakeProjectIssues{issues: []Issue{
		phaseIssue("FRG-1", "completed", "P1", 1, ""),
		phaseIssue("FRG-2", "completed", "P1", 1, ""),
		phaseIssue("FRG-3", "started", "P2", 2, "agent-1"),
		blocked,
		phaseIssue("FRG-5", "unstarted", "P2", 2, ""),
		phaseIssue("FRG-6", "unstarted", "P2", 2, "agent-1"),
		phaseIssue("FRG-7", "unstarted", "P3", 3, ""),
	}}
	runs := fakeRuns{"FRG-3": {Stage: PhaseStageBuilding}, "FRG-6": {Queued: true}}
	pollProjects(context.Background(), phaseDeps(&fakePhaseProjects{listed: []PhaseProject{{Repository: "octo/widget", LinearProject: "Roadmap"}}}, snaps, runs, issues))

	if len(snaps.got) != 1 {
		t.Fatalf("recorded %d snapshots, want 1", len(snaps.got))
	}
	got := snaps.got[0]
	wantPhases := []PhaseProgress{{"P1", 2, 2}, {"P2", 0, 4}, {"P3", 0, 1}}
	if !slices.Equal(got.Phases, wantPhases) || got.Current != "P2" {
		t.Errorf("phases = %v current %q, want %v current P2", got.Phases, got.Current, wantPhases)
	}
	if want := []PhaseTicket{{"FRG-3", "building"}}; !slices.Equal(got.InFlight, want) || got.Queued != 1 || got.Blocked != 1 {
		t.Errorf("in flight %v queued %d blocked %d", got.InFlight, got.Queued, got.Blocked)
	}
	if want := []string{"FRG-4", "FRG-5"}; !slices.Equal(got.Eligible, want) {
		t.Errorf("eligible = %v, want %v", got.Eligible, want)
	}
}

func TestStartDelegatesTheCurrentPhaseOnceAndIsIdempotent(t *testing.T) {
	projects := &fakePhaseProjects{listed: []PhaseProject{widget("P1")}}
	issues := &fakeProjectIssues{issues: []Issue{
		phaseIssue("FRG-1", "unstarted", "P1", 1, ""),
		phaseIssue("FRG-2", "unstarted", "P1", 1, "agent-1"),
		phaseIssue("FRG-3", "unstarted", "P2", 2, ""),
	}}
	d := phaseDeps(projects, &fakeSnapshots{}, fakeRuns{}, issues)
	pollProjects(context.Background(), d)
	if want := []string{"uuid-FRG-1"}; !slices.Equal(issues.delegated, want) {
		t.Fatalf("delegated %v, want %v: another phase's ticket or one already delegated was taken", issues.delegated, want)
	}
	if want := []string{"octo/widget:"}; !slices.Equal(projects.resolved, want) {
		t.Fatalf("resolved %v, want %v", projects.resolved, want)
	}

	issues.issues[0].DelegatedTo = "agent-1"
	pollProjects(context.Background(), d)
	if len(issues.delegated) != 1 {
		t.Errorf("delegated %v after a second poll, want nothing more", issues.delegated)
	}
}

func TestStartIsRefusedWhenThePhaseMoved(t *testing.T) {
	projects := &fakePhaseProjects{listed: []PhaseProject{widget("P1")}}
	issues := &fakeProjectIssues{issues: []Issue{
		phaseIssue("FRG-1", "completed", "P1", 1, ""),
		phaseIssue("FRG-2", "unstarted", "P2", 2, ""),
	}}
	pollProjects(context.Background(), phaseDeps(projects, &fakeSnapshots{}, fakeRuns{}, issues))
	if len(issues.delegated) != 0 {
		t.Fatalf("delegated %v, want nothing once the confirmed phase is done", issues.delegated)
	}
	if want := []string{"octo/widget:phase-moved"}; !slices.Equal(projects.resolved, want) {
		t.Errorf("resolved %v, want %v", projects.resolved, want)
	}
}

func TestStartDelegatesNothingWhenLinearCannotBeRead(t *testing.T) {
	projects := &fakePhaseProjects{listed: []PhaseProject{widget("P1")}}
	snaps := &fakeSnapshots{}
	issues := &fakeProjectIssues{err: errors.New("page 2 failed"), issues: []Issue{phaseIssue("FRG-1", "unstarted", "P1", 1, "")}}
	pollProjects(context.Background(), phaseDeps(projects, snaps, fakeRuns{}, issues))
	if len(issues.delegated) != 0 {
		t.Fatalf("delegated %v from a failed read", issues.delegated)
	}
	if len(snaps.got) != 1 || !snaps.got[0].Unreadable || snaps.got[0].Reason != StartRefusedUnreadable || !snaps.got[0].At.Equal(phaseNow) {
		t.Errorf("snapshot = %+v, want unreadable with its reason and time", snaps.got)
	}
	if want := []string{"octo/widget:linear-unreadable"}; !slices.Equal(projects.resolved, want) {
		t.Errorf("resolved %v, want %v", projects.resolved, want)
	}
}

func TestProjectIssuesFailsOnAPartialRead(t *testing.T) {
	refuseSecond := func(n int, _ string) bool { return n == 1 }
	srv, _ := gatedServer(t, refuseSecond,
		linearReply("agent-1", true, "cursor-1", linearNode("FRG-1", 2, "size:S")),
		linearReply("agent-1", false, "", linearNode("FRG-2", 2, "size:S")))
	l := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1", Client: srv.Client(), Page: 1}
	issues, err := l.ProjectIssues(context.Background(), []string{"Roadmap"})
	if err == nil || issues != nil {
		t.Fatalf("ProjectIssues = %v, %v, want no issues from a read that lost a page", issues, err)
	}
}

func TestDelegateIssueFailsUnlessLinearReportsTheDelegate(t *testing.T) {
	reply := func(delegate string, success bool) map[string]any {
		return map[string]any{"data": map[string]any{"issueUpdate": map[string]any{
			"success": success, "issue": map[string]any{"delegate": map[string]any{"id": delegate}}}}}
	}
	for name, tt := range map[string]struct {
		delegate string
		success  bool
		wantErr  bool
	}{
		"delegated to the agent":    {"agent-1", true, false},
		"delegated to someone else": {"someone-else", true, true},
		"reported unsuccessful":     {"agent-1", false, true},
	} {
		t.Run(name, func(t *testing.T) {
			srv, seen := queryServer(t, reply(tt.delegate, tt.success))
			l := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("t"), Delegate: "agent-1", Client: srv.Client()}
			err := l.DelegateIssue(context.Background(), "uuid-1")
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, want error %v", err, tt.wantErr)
			}
			if got := (*seen)[0].query; !strings.Contains(got, "delegateId") {
				t.Errorf("the mutation does not set the delegate: %s", got)
			}
		})
	}
}

func TestStartIsRefusedWhenNothingIsEligible(t *testing.T) {
	projects := &fakePhaseProjects{listed: []PhaseProject{widget("P1")}}
	held := phaseIssue("FRG-1", "unstarted", "P1", 1, "")
	held.Labels = append(held.Labels, holdLabel)
	issues := &fakeProjectIssues{issues: []Issue{held}}
	pollProjects(context.Background(), phaseDeps(projects, &fakeSnapshots{}, fakeRuns{}, issues))
	if len(issues.delegated) != 0 {
		t.Fatalf("delegated %v, want nothing", issues.delegated)
	}
	if want := []string{"octo/widget:no-eligible-ticket"}; !slices.Equal(projects.resolved, want) {
		t.Errorf("resolved %v, want %v", projects.resolved, want)
	}
}

func TestAProjectWithNoPendingRequestIsSnapshottedAndLeftAlone(t *testing.T) {
	p := widget("")
	p.StartRequestedAt = time.Time{}
	projects := &fakePhaseProjects{listed: []PhaseProject{p}}
	issues := &fakeProjectIssues{issues: []Issue{phaseIssue("FRG-1", "unstarted", "P1", 1, "")}}
	snaps := &fakeSnapshots{}
	pollProjects(context.Background(), phaseDeps(projects, snaps, fakeRuns{}, issues))
	if len(snaps.got) != 1 || len(issues.delegated) != 0 || len(projects.resolved) != 0 {
		t.Errorf("snapshots %d, delegated %v, resolved %v: want a snapshot and nothing else", len(snaps.got), issues.delegated, projects.resolved)
	}
}

func TestPollProjectsReadsNothingWithoutListedProjects(t *testing.T) {
	issues := &fakeProjectIssues{}
	pollProjects(context.Background(), phaseDeps(&fakePhaseProjects{}, &fakeSnapshots{}, fakeRuns{}, issues))
	if issues.reads != 0 {
		t.Errorf("Linear was read %d times with no project listed", issues.reads)
	}
}

func TestACompletedTicketIsNeverInFlightOrQueued(t *testing.T) {
	snaps := &fakeSnapshots{}
	issues := &fakeProjectIssues{issues: []Issue{
		phaseIssue("FRG-1", "completed", "P1", 1, ""),
		phaseIssue("FRG-2", "unstarted", "P1", 1, ""),
	}}
	runs := fakeRuns{"FRG-1": {Stage: PhaseStageBuilding}}
	pollProjects(context.Background(), phaseDeps(&fakePhaseProjects{listed: []PhaseProject{{Repository: "octo/widget", LinearProject: "Roadmap"}}}, snaps, runs, issues))
	if got := snaps.got[0]; len(got.InFlight) != 0 || got.Queued != 0 {
		t.Errorf("in flight %v queued %d, want the completed ticket left out", got.InFlight, got.Queued)
	}
}

func TestPhaseProgressLeavesCanceledTicketsOut(t *testing.T) {
	progress, current := phaseProgress([]Issue{
		phaseIssue("FRG-1", "completed", "P1", 1, ""),
		phaseIssue("FRG-2", "canceled", "P1", 1, ""),
	})
	if want := []PhaseProgress{{"P1", 1, 1}}; !slices.Equal(progress, want) || current != "" {
		t.Errorf("progress %v current %q, want %v with no current phase", progress, current, want)
	}
}

func TestPollReadsProjectPhasesOnlyAgainstAUsableAllowlist(t *testing.T) {
	for name, tt := range map[string]struct {
		cfg   Config
		reads int
	}{
		"empty":      {Config{}, 0},
		"unreadable": {Config{ReposErr: errFirestore}, 0},
		"usable":     {Config{Repos: []string{"octo/widget"}}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			issues := &fakeProjectIssues{}
			deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
			deps.ProjectPhases = ProjectPhaseDeps{
				Projects:  &fakePhaseProjects{listed: []PhaseProject{{Repository: "octo/widget", LinearProject: "Roadmap"}}},
				Snapshots: &fakeSnapshots{}, Runs: fakeRuns{}, Issues: issues,
			}
			if _, err := Poll(context.Background(), deps, tt.cfg); err != nil {
				t.Fatal(err)
			}
			if issues.reads != tt.reads {
				t.Errorf("Linear read %d times, want %d", issues.reads, tt.reads)
			}
		})
	}
}
