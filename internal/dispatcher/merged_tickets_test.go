package dispatcher

import (
	"slices"
	"testing"
	"time"
)

func TestMergedTicketsMatchesOnlyAWholeIDInAMergedTitle(t *testing.T) {
	merged := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	open := func(id string) Issue { return Issue{ID: id, StateType: "started"} }
	tests := []struct {
		name   string
		issues []Issue
		prs    []ClosedPR
		want   []string
	}{
		{"id after a type prefix", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat(x): FRG-12 add y", MergedAt: merged}}, []string{"FRG-12"}},
		{"id at the start", []Issue{open("FRG-12")}, []ClosedPR{{Title: "FRG-12 add y", MergedAt: merged}}, []string{"FRG-12"}},
		{"id at the end", []Issue{open("FRG-12")}, []ClosedPR{{Title: "add y for FRG-12", MergedAt: merged}}, []string{"FRG-12"}},
		{"id in brackets", []Issue{open("FRG-12")}, []ClosedPR{{Title: "fix: add y (FRG-12).", MergedAt: merged}}, []string{"FRG-12"}},
		{"id inside a longer id", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat: FRG-123 add y", MergedAt: merged}}, nil},
		{"id after a prefix letter", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat: XFRG-12 add y", MergedAt: merged}}, nil},
		{"id before a suffix", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat: FRG-12-b add y", MergedAt: merged}}, nil},
		{"whole id after a longer one", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat: FRG-123 and FRG-12", MergedAt: merged}}, []string{"FRG-12"}},
		{"unmerged pull request", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat: FRG-12 add y"}}, nil},
		{"revert of a pull request naming the id", []Issue{open("FRG-12")}, []ClosedPR{{Title: `Revert "feat: FRG-12 add y"`, MergedAt: merged}}, nil},
		{"id in the body only", []Issue{open("FRG-12")}, []ClosedPR{{Title: "feat: add y", Body: "FRG-12", MergedAt: merged}}, nil},
		{"completed issue", []Issue{{ID: "FRG-12", StateType: "completed"}}, []ClosedPR{{Title: "feat: FRG-12 add y", MergedAt: merged}}, nil},
		{"canceled issue", []Issue{{ID: "FRG-12", StateType: "canceled"}}, []ClosedPR{{Title: "feat: FRG-12 add y", MergedAt: merged}}, nil},
		{"two pull requests naming one id", []Issue{open("FRG-12")}, []ClosedPR{
			{Title: "feat: FRG-12 part one", MergedAt: merged}, {Title: "feat: FRG-12 part two", MergedAt: merged}}, []string{"FRG-12"}},
		{"two ids in one title", []Issue{open("FRG-12"), open("FRG-13"), open("FRG-14")}, []ClosedPR{
			{Title: "feat: FRG-12 and FRG-13", MergedAt: merged}}, []string{"FRG-12", "FRG-13"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergedTickets(tt.issues, tt.prs); !slices.Equal(got, tt.want) {
				t.Errorf("mergedTickets = %v, want %v", got, tt.want)
			}
		})
	}
}
