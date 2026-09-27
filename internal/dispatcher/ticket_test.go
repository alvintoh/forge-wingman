package dispatcher

import (
	"testing"
	"time"
)

var buildAt = time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)

// admitted is an issue a Linear workspace would hand over delegated.
func admitted(labels ...string) Issue {
	return Issue{
		ID:       "FRG-18",
		Title:    "feat(dispatcher): poll Linear and dispatch runs",
		Body:     "Enqueue delegated tickets, then dispatch the highest-priority run.\n",
		Priority: 2,
		Labels:   labels,
	}
}

var buildConfig = Config{Repos: []string{"AlvinToh/Forge-Wingman", "octo/scratch"}}

// buildCase is one issue and the refusal it must draw, under a config of its
// own where it needs one.
type buildCase struct {
	issue  Issue
	reason Refusal
	repos  []string
}

func TestBuildAdmitsASizedTicketForAnAllowlistedRepository(t *testing.T) {
	q, rejection := build(admitted("size:M", "repo:octo/Scratch"), buildConfig, buildAt)
	if rejection.Reason != "" {
		t.Fatalf("refused %s: %s", rejection.Reason, rejection.Detail)
	}
	// The repository is the allowlist's own spelling, so a label's casing cannot
	// move a run somewhere the allowlist never named.
	if q.RunID != "FRG-18" || q.Repo != "octo/scratch" || q.Priority != 2 || !q.At.Equal(buildAt) {
		t.Fatalf("queued = %+v", q)
	}
	tk := q.Ticket
	if tk.ID != "FRG-18" || tk.Size != "M" || tk.Title == "" || tk.Body == "" || tk.SizedBy != "linear-label" {
		t.Fatalf("ticket = %+v", tk)
	}
	if err := tk.Validate(); err != nil {
		t.Fatalf("the runner cannot build the ticket: %v", err)
	}
}

func TestBuildRefuses(t *testing.T) {
	unsized := admitted("size:XL", "repo:octo/scratch")
	noBody := admitted("size:S", "repo:octo/scratch")
	noBody.Body = ""
	branchUnsafe := admitted("size:S", "repo:octo/scratch")
	branchUnsafe.ID = "FRG/18"
	offScale := admitted("size:S", "repo:octo/scratch")
	offScale.Priority = 7

	for name, tt := range map[string]buildCase{
		"no size label":                      {admitted("repo:octo/scratch"), RefusalNoSize, nil},
		"an empty size label":                {admitted("size:", "repo:octo/scratch"), RefusalNoSize, nil},
		"a size above the ceiling":           {unsized, RefusalAboveCeiling, nil},
		"a size outside the ladder":          {admitted("size:enormous", "repo:octo/scratch"), RefusalSizeUnknown, nil},
		"no repository label":                {admitted("size:S"), RefusalNoRepository, nil},
		"a repository outside the allowlist": {admitted("size:S", "repo:someone/else"), RefusalNotAllowlist, nil},
		"an allowlist that names nothing":    {admitted("size:S", "repo:octo/scratch"), RefusalNotAllowlist, nil},
		"an empty description":               {noBody, RefusalTicketInvalid, nil},
		"an id that cannot name a record":    {branchUnsafe, RefusalTicketInvalid, nil},
		"a priority Linear does not hold":    {offScale, RefusalTicketInvalid, nil},
	} {
		t.Run(name, func(t *testing.T) {
			c := buildConfig
			if name == "an allowlist that names nothing" {
				c.Repos = nil
			}
			q, rejection := build(tt.issue, c, buildAt)
			if rejection.Reason != tt.reason {
				t.Fatalf("reason = %q, want %q (%s)", rejection.Reason, tt.reason, rejection.Detail)
			}
			if rejection.Ticket != tt.issue.ID || rejection.Detail == "" || !rejection.At.Equal(buildAt) {
				t.Fatalf("rejection = %+v", rejection)
			}
			if q.RunID != "" {
				t.Fatalf("a refused ticket was queued: %+v", q)
			}
		})
	}
}

func TestBuildRefusesTheRepositoryBeforeTheSize(t *testing.T) {
	// A ticket naming a repository the dispatcher may not dispatch into is
	// refused for that, whichever size it carries.
	_, rejection := build(admitted("size:XL", "repo:someone/else"), buildConfig, buildAt)
	if rejection.Reason != RefusalNotAllowlist {
		t.Fatalf("reason = %q, want %q", rejection.Reason, RefusalNotAllowlist)
	}
}

func TestPriorityRankSortsNoPriorityLast(t *testing.T) {
	for _, tt := range []struct {
		priority int
		want     int
	}{{0, noPriorityRank}, {1, 1}, {4, 4}} {
		got, err := priorityRank(tt.priority)
		if err != nil || got != tt.want {
			t.Errorf("priorityRank(%d) = %d, %v, want %d", tt.priority, got, err, tt.want)
		}
	}
}

func TestLabelValueTakesTheFirstMatch(t *testing.T) {
	got, ok := labelValue([]string{"size:M", "size:XL", "repo:octo/scratch"}, sizePrefix)
	if !ok || got != "M" {
		t.Fatalf("labelValue = %q, %v", got, ok)
	}
	if _, ok := labelValue([]string{"size:M"}, repoPrefix); ok {
		t.Fatal("a label of another kind was read")
	}
}
