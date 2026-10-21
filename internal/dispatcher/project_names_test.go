package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// fakeLinearProjects is the Linear projects a poll reads, and whether the read
// fails.
type fakeLinearProjects struct {
	names []string
	err   error
}

func (f *fakeLinearProjects) ProjectNames(context.Context) ([]string, error) {
	return f.names, f.err
}

// fakeProjectRecords keeps every record a poll wrote, and fails the write it is
// given an error for.
type fakeProjectRecords struct {
	writes [][]string
	err    error
}

func (f *fakeProjectRecords) RecordProjectNames(_ context.Context, names []string) error {
	if f.err != nil {
		return f.err
	}
	f.writes = append(f.writes, names)
	return nil
}

func TestPollRecordsTheLinearProjectNames(t *testing.T) {
	records := &fakeProjectRecords{}
	deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	deps.LinearProjects = &fakeLinearProjects{names: []string{"Forge", "Atlas"}}
	deps.ProjectRecords = records
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(records.writes) != 1 || !slices.Equal(records.writes[0], []string{"Forge", "Atlas"}) {
		t.Fatalf("recorded %v, want the names the poll read", records.writes)
	}
}

func TestPollKeepsTheLastProjectNamesWhenTheReadFails(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	records := &fakeProjectRecords{}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.LinearProjects = &fakeLinearProjects{err: errFirestore}
	deps.ProjectRecords = records
	res, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}})
	if err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed read", err)
	}
	if len(records.writes) != 0 {
		t.Fatalf("recorded %v, want the last record kept", records.writes)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) {
		t.Fatalf("dispatched %v, want the ticket dispatched", res.Dispatched)
	}
}

func TestPollKeepsRunningWhenTheProjectNamesCannotBeRecorded(t *testing.T) {
	var logs strings.Builder
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	deps.LinearProjects = &fakeLinearProjects{names: []string{"Forge"}}
	deps.ProjectRecords = &fakeProjectRecords{err: errFirestore}
	res, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}})
	if err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed write", err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) {
		t.Fatalf("dispatched %v, want the ticket dispatched", res.Dispatched)
	}
	if !strings.Contains(logs.String(), "level=WARN msg=linearProjectsNotRecorded") {
		t.Fatalf("logs:\n%s\nwant a linearProjectsNotRecorded warning", logs.String())
	}
}

func TestPollRecordsTheProjectNamesWhenTheAllowlistCannotBeRead(t *testing.T) {
	records := &fakeProjectRecords{}
	deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	deps.LinearProjects = &fakeLinearProjects{names: []string{"Forge"}}
	deps.ProjectRecords = records
	if _, err := Poll(context.Background(), deps, Config{ReposErr: errFirestore}); err != nil {
		t.Fatal(err)
	}
	if len(records.writes) != 1 || !slices.Equal(records.writes[0], []string{"Forge"}) {
		t.Fatalf("recorded %v, want the names recorded with no allowlist read", records.writes)
	}
}

func TestPollDispatchesAndRecordsTheRestTheSameWithOrWithoutTheProjectNames(t *testing.T) {
	run := func(withNames bool) string {
		q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
		deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
		reach, snaps := &fakeReach{}, &fakeSnapshots{}
		deps.Reach = reach
		deps.ProjectPhases = ProjectPhaseDeps{
			Projects:  &fakePhaseProjects{listed: []PhaseProject{{Repository: "octo/widget", LinearProject: "Roadmap"}}},
			Snapshots: snaps,
			Runs:      fakeRuns{},
			Issues:    &fakeProjectIssues{},
		}
		if withNames {
			deps.LinearProjects = &fakeLinearProjects{names: []string{"Roadmap"}}
			deps.ProjectRecords = &fakeProjectRecords{}
		}
		res, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%v|%d|%+v|%+v", res.Dispatched, len(q.tryClaims), reach.recorded, snaps.got)
	}
	if with, without := run(true), run(false); with != without {
		t.Fatalf("with the record = %q, without = %q, want dispatch, reach and snapshots unchanged", with, without)
	}
}
