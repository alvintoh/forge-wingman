package dispatcher

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
)

// delegateLane matches the lane a delegatable title opens with: the ticket
// templates' [BE], [FE] and [SLICE], read in any case as the runner reads a
// ticket's own lane tag. [INFRA] and [OPS] never match.
var delegateLane = regexp.MustCompile(`(?i)^\[(BE|FE|SLICE)\]`)

// workflowPath is the repository path a ticket's body must not name, since no
// run may edit the workflow it is dispatched by.
const workflowPath = ".github/workflows/"

// holdLabel takes a ticket out of every start: the owner's hold.
const holdLabel = "wingman:hold"

// The stages a run in flight shows, and the reasons a start request is refused.
const (
	PhaseStagePlanning = "planning"
	PhaseStageBuilding = "building"

	StartRefusedPhaseMoved  = "phase-moved"
	StartRefusedUnreadable  = "linear-unreadable"
	StartRefusedUnreachable = "repository-unreachable"
	StartRefusedNoPhase     = "no-current-phase"
	StartRefusedNoEligible  = "no-eligible-ticket"
	SnapshotRunsUnreadable  = "runs-unreadable"
)

// Milestone is the Linear project milestone a ticket belongs to: a phase.
type Milestone struct {
	ID        string
	Name      string
	SortOrder float64
}

// PhaseProject is one listed project as the phase poll and the surface read it.
type PhaseProject struct {
	Repository    string
	LinearProject string
	AddedAt       time.Time
	Paused        bool
	// StartPhase and StartRequestedAt are the owner's pending request to start a
	// phase, zero when none is pending; StartRefusal is why the last one was not
	// carried out.
	StartPhase       string
	StartRequestedAt time.Time
	StartRefusal     string
}

// PhaseProgress is one phase's completed and total tickets.
type PhaseProgress struct {
	Name  string `firestore:"name" json:"name"`
	Done  int    `firestore:"done" json:"done"`
	Total int    `firestore:"total" json:"total"`
}

// PhaseTicket is a ticket of the current phase with a run in flight, and the
// stage that run is in.
type PhaseTicket struct {
	ID    string `firestore:"id" json:"id"`
	Title string `firestore:"title" json:"title"`
	Stage string `firestore:"stage" json:"stage"`
}

// PhaseSnapshot is what one poll read of a project's phases. An unreadable one
// says why and when, and carries nothing else.
type PhaseSnapshot struct {
	Repository string          `firestore:"repository" json:"repository"`
	Phases     []PhaseProgress `firestore:"phases" json:"phases"`
	// Current is the first phase not fully done, empty when every phase is.
	Current  string        `firestore:"current" json:"current"`
	InFlight []PhaseTicket `firestore:"in_flight" json:"in_flight"`
	Queued   int           `firestore:"queued" json:"queued"`
	// QueuedBlocked is how many of the queued tickets wait on an open blocker.
	QueuedBlocked int `firestore:"queued_blocked" json:"queued_blocked"`
	// Eligible is the ids of the current phase's tickets a start would delegate,
	// and EligibleBlocked how many of them wait on an open blocker.
	Eligible        []string  `firestore:"eligible" json:"eligible"`
	EligibleBlocked int       `firestore:"eligible_blocked" json:"eligible_blocked"`
	Unreadable      bool      `firestore:"unreadable" json:"unreadable"`
	Reason          string    `firestore:"reason" json:"reason"`
	At              time.Time `firestore:"at" json:"at"`
}

// TicketRun is where one ticket's run row stands: ended (stopped, abandoned or
// carrying an outcome, so only an operator's requeue takes it further), waiting
// in the queue, or in flight at a stage.
type TicketRun struct {
	Ended  bool
	Queued bool
	Stage  string
}

// ProjectPhaseDeps are the optional dependencies of the phase poll; it runs
// only when every one is set.
type ProjectPhaseDeps struct {
	// Projects lists the listed projects and settles a start request.
	Projects PhaseProjects
	// Snapshots stores each poll's snapshots.
	Snapshots PhaseSnapshots
	// Runs reads the run rows of tickets.
	Runs PhaseRuns
	// Issues reads the projects' tickets and delegates one.
	Issues ProjectIssues
}

func (p ProjectPhaseDeps) ready() bool {
	return p.Projects != nil && p.Snapshots != nil && p.Runs != nil && p.Issues != nil
}

// PhaseProjects reads the owner's listed projects and settles a start request:
// ResolveStart clears the request made at requestedAt, recording refusal against
// the project when it is not empty, and leaves a newer request alone.
type PhaseProjects interface {
	Listed(ctx context.Context) ([]PhaseProject, error)
	ResolveStart(ctx context.Context, repository string, requestedAt time.Time, refusal string) error
}

// PhaseSnapshots stores a poll's snapshots, one per project.
type PhaseSnapshots interface {
	RecordSnapshots(ctx context.Context, snapshots []PhaseSnapshot) error
}

// PhaseRuns reads the run rows of the named tickets; a ticket with none is absent.
type PhaseRuns interface {
	TicketRuns(ctx context.Context, tickets []string) (map[string]TicketRun, error)
}

// ProjectIssues is Linear as the phase poll uses it: the tickets of the named
// projects, and a delegation.
type ProjectIssues interface {
	ProjectIssues(ctx context.Context, projects []string) ([]Issue, error)
	DelegateIssue(ctx context.Context, issueID string) error
}

// phaseView is one project's snapshot with the tickets a start would delegate.
type phaseView struct {
	snapshot PhaseSnapshot
	eligible []Issue
}

// pollProjects reads Linear once for every listed project, writes each
// project's snapshot, and carries out each pending start request. A Linear read
// that fails writes an unreadable snapshot and delegates nothing; no failure
// here fails the poll. A start on a repository this poll found unreachable is
// refused.
func pollProjects(ctx context.Context, d Deps, reaches []RepoReach) {
	deps := d.ProjectPhases
	if !deps.ready() {
		return
	}
	projects, err := deps.Projects.Listed(ctx)
	if err != nil {
		d.Logger.Warn("phaseProjectsUnread", "err", err.Error())
		return
	}
	if len(projects) == 0 {
		return
	}
	at := d.Now()
	issues, err := deps.Issues.ProjectIssues(ctx, linearProjects(projects))
	if err != nil {
		d.Logger.Warn("phaseIssuesUnread", "err", err.Error())
	}
	views := make([]phaseView, 0, len(projects))
	for _, p := range projects {
		if err != nil {
			views = append(views, phaseView{snapshot: unreadableSnapshot(p.Repository, StartRefusedUnreadable, at)})
			continue
		}
		views = append(views, projectView(ctx, d, p, issues, at))
	}
	snapshots := make([]PhaseSnapshot, 0, len(views))
	for _, v := range views {
		snapshots = append(snapshots, v.snapshot)
	}
	if err := deps.Snapshots.RecordSnapshots(ctx, snapshots); err != nil {
		d.Logger.Warn("phaseSnapshotsNotRecorded", "projects", len(snapshots), "err", err.Error())
	}
	unreachable := map[string]bool{}
	for _, r := range reaches {
		if !r.Reachable {
			unreachable[strings.ToLower(r.Repository)] = true
		}
	}
	for i, p := range projects {
		if !p.StartRequestedAt.IsZero() {
			startPhase(ctx, d, p, views[i], unreachable[strings.ToLower(p.Repository)])
		}
	}
}

// linearProjects is the distinct Linear project names the listed projects name.
func linearProjects(projects []PhaseProject) []string {
	var names []string
	for _, p := range projects {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, p.LinearProject) }) {
			names = append(names, p.LinearProject)
		}
	}
	return names
}

func unreadableSnapshot(repository, reason string, at time.Time) PhaseSnapshot {
	return PhaseSnapshot{Repository: repository, Unreadable: true, Reason: reason, At: at}
}

// projectView reads the run rows of the project's open tickets in its current
// phase and builds the snapshot; a run store that cannot be read leaves it
// unreadable, since the eligibility rule needs the rows.
func projectView(ctx context.Context, d Deps, p PhaseProject, all []Issue, at time.Time) phaseView {
	var issues []Issue
	for _, issue := range all {
		if strings.EqualFold(issue.Project, p.LinearProject) {
			issues = append(issues, issue)
		}
	}
	progress, current := phaseProgress(issues)
	var open []Issue
	var ids []string
	for _, issue := range issues {
		if current != "" && issue.Milestone.Name == current && issue.StateType != stateTypeCompleted && issue.StateType != stateTypeCanceled {
			open = append(open, issue)
			ids = append(ids, issue.ID)
		}
	}
	runs := map[string]TicketRun{}
	if len(ids) > 0 {
		var err error
		if runs, err = d.ProjectPhases.Runs.TicketRuns(ctx, ids); err != nil {
			d.Logger.Warn("phaseRunsUnread", "repo", p.Repository, "err", err.Error())
			return phaseView{snapshot: unreadableSnapshot(p.Repository, SnapshotRunsUnreadable, at)}
		}
	}
	v := phaseView{snapshot: PhaseSnapshot{
		Repository: p.Repository, Phases: progress, Current: current,
		InFlight: []PhaseTicket{}, Eligible: []string{}, At: at,
	}}
	for _, issue := range open {
		run := runs[issue.ID]
		blocked := len(issue.BlockedByOpen) > 0
		switch {
		case run.Stage != "":
			v.snapshot.InFlight = append(v.snapshot.InFlight, PhaseTicket{ID: issue.ID, Title: issue.Title, Stage: run.Stage})
		case run.Queued:
			v.snapshot.Queued++
			if blocked {
				v.snapshot.QueuedBlocked++
			}
		}
		if phaseEligible(issue, p.Repository, run) {
			v.eligible = append(v.eligible, issue)
			v.snapshot.Eligible = append(v.snapshot.Eligible, issue.ID)
			if blocked {
				v.snapshot.EligibleBlocked++
			}
		}
	}
	return v
}

// phaseProgress is each milestone's done and total tickets in Linear's own
// milestone order, and the name of the first one not fully done. A ticket in no
// milestone belongs to no phase.
func phaseProgress(issues []Issue) (progress []PhaseProgress, current string) {
	type phase struct {
		order float64
		PhaseProgress
	}
	var phases []phase
	for _, issue := range issues {
		if issue.Milestone.ID == "" || issue.StateType == stateTypeCanceled {
			continue
		}
		i := slices.IndexFunc(phases, func(p phase) bool { return p.Name == issue.Milestone.Name })
		if i < 0 {
			phases = append(phases, phase{order: issue.Milestone.SortOrder, PhaseProgress: PhaseProgress{Name: issue.Milestone.Name}})
			i = len(phases) - 1
		}
		phases[i].Total++
		if issue.StateType == stateTypeCompleted {
			phases[i].Done++
		}
	}
	slices.SortFunc(phases, func(a, b phase) int {
		switch {
		case a.order < b.order:
			return -1
		case a.order > b.order:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, p := range phases {
		progress = append(progress, p.PhaseProgress)
		if current == "" && p.Done < p.Total {
			current = p.Name
		}
	}
	return progress, current
}

// phaseEligible reports whether a start may delegate issue: a code-lane ticket
// with a size and the project's repository label, that nobody has begun, whose
// body names no workflow path and no hold label, with no delegate yet and no run
// row that has ended, is queued or is in flight.
func phaseEligible(issue Issue, repo string, run TicketRun) bool {
	if !delegateLane.MatchString(issue.Title) ||
		// An unknown state reads as started.
		!slices.Contains(unstartedStates, issue.StateType) ||
		strings.Contains(issue.Body, workflowPath) ||
		issue.DelegatedTo != "" || run.Ended || run.Queued || run.Stage != "" ||
		slices.ContainsFunc(issue.Labels, func(l string) bool { return strings.EqualFold(l, holdLabel) }) {
		return false
	}
	if _, reason, _ := repository(issue.Labels, []string{repo}); reason != "" {
		return false
	}
	_, reason, _ := sizeLabel(issue.Labels)
	return reason == ""
}

// startPhase carries out p's pending start request against the snapshot just
// read: it is refused when Linear could not be read, when the repository is
// unreachable, when the project has no current phase, when the current phase is
// no longer the one the owner confirmed, or when nothing is eligible; otherwise
// every eligible ticket is delegated. A delegation that fails leaves the request
// pending, and the next poll skips what was already delegated.
func startPhase(ctx context.Context, d Deps, p PhaseProject, v phaseView, unreachable bool) {
	refusal := ""
	switch {
	case v.snapshot.Unreadable:
		refusal = v.snapshot.Reason
	case unreachable:
		refusal = StartRefusedUnreachable
	case v.snapshot.Current == "":
		refusal = StartRefusedNoPhase
	case v.snapshot.Current != p.StartPhase:
		refusal = StartRefusedPhaseMoved
	case len(v.eligible) == 0:
		refusal = StartRefusedNoEligible
	}
	if refusal == "" {
		delegated := 0
		for _, issue := range v.eligible {
			if err := d.ProjectPhases.Issues.DelegateIssue(ctx, issue.UUID); err != nil {
				d.Logger.Error("phaseDelegationFailed", "repo", p.Repository, "phase", p.StartPhase,
					"delegated", delegated, "err", err.Error())
				return
			}
			delegated++
		}
		d.Logger.Info("phaseStarted", "repo", p.Repository, "phase", p.StartPhase,
			"eligible", len(v.eligible), "delegated", delegated)
	} else {
		d.Logger.Warn("phaseStartRefused", "repo", p.Repository, "phase", p.StartPhase, "reason", refusal)
	}
	if err := d.ProjectPhases.Projects.ResolveStart(ctx, p.Repository, p.StartRequestedAt, refusal); err != nil {
		d.Logger.Warn("phaseStartNotSettled", "repo", p.Repository, "err", err.Error())
	}
}
