package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// fakeAutonomies reports the level configured, or fails to read it, and counts
// the reads made. reads is a pointer so a copy in Deps still counts.
type fakeAutonomies struct {
	level Autonomy
	err   error
	reads *int
}

func (a fakeAutonomies) Level(context.Context) (Autonomy, error) {
	if a.reads != nil {
		*a.reads++
	}
	return a.level, a.err
}

func TestAutonomyLevels(t *testing.T) {
	for level, want := range map[Autonomy]struct {
		valid, autoMerges bool
		settable          error
	}{
		-1: {false, false, ErrUnknownAutonomy},
		0:  {true, false, nil},
		3:  {true, false, nil},
		4:  {true, true, ErrAutonomyNotEarned},
		5:  {true, true, ErrAutonomyNotEarned},
		6:  {false, false, ErrUnknownAutonomy},
	} {
		if got := level.Valid(); got != want.valid {
			t.Errorf("Autonomy(%d).Valid() = %v, want %v", level, got, want.valid)
		}
		if got := level.AutoMerges(); got != want.autoMerges {
			t.Errorf("Autonomy(%d).AutoMerges() = %v, want %v", level, got, want.autoMerges)
		}
		if err := level.Settable(); !errors.Is(err, want.settable) || (err == nil) != (want.settable == nil) {
			t.Errorf("Autonomy(%d).Settable() = %v, want %v", level, err, want.settable)
		}
		if got := level.Describe(); (got != "") != want.valid {
			t.Errorf("Autonomy(%d).Describe() = %q, want a line only for a level that exists", level, got)
		}
	}
}

func TestAutonomyRefusalNamesTheAdvanceCondition(t *testing.T) {
	for level, want := range map[Autonomy]string{
		4: "level 4 needs level 3 held, a rewrite rate of 5% or less over 10 PRs, zero gate misses, and CD on the target repository",
		5: "level 5 needs level 4 held over 20 sampled PRs with zero reverts",
	} {
		if err := level.Settable(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Autonomy(%d).Settable() = %v, want it to contain %q", level, err, want)
		}
	}
}

func TestPollClaimsAtTheLevelItReadOnce(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "run-a", Repo: "octo/scratch", Size: "M"}, {RunID: "run-b", Repo: "octo/other", Size: "M"},
	}}
	var reads int
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Autonomies = fakeAutonomies{level: 3, reads: &reads}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(q.tryClaims) != 2 || q.tryClaims[0].facts.Autonomy != 3 || q.tryClaims[1].facts.Autonomy != 3 {
		t.Fatalf("reads = %d, tryClaims = %+v, want one read and both claims at level 3", reads, q.tryClaims)
	}
}

func TestPollClaimsAtLevelZeroWhenTheLevelCannotBeRead(t *testing.T) {
	for name, autonomies := range map[string]fakeAutonomies{
		"the read fails":   {level: 3, err: errFirestore},
		"an unknown level": {level: 9},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
			var logs bytes.Buffer
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.Autonomies = autonomies
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].facts.Autonomy != 0 {
				t.Fatalf("tryClaims = %+v, want the run claimed at level 0", q.tryClaims)
			}
			if !strings.Contains(logs.String(), "msg=autonomyUnread") {
				t.Fatalf("log = %q, want autonomyUnread", logs.String())
			}
		})
	}
}
