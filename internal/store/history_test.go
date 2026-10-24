package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// historyBase is later than any row another test writes, so the history reads
// only this test's rows.
var historyBase = time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)

func TestRecentSettledListsTheNewestSettledRunsWithHowEachEnded(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("history-%d", time.Now().UnixNano())
	settled := func(age time.Duration, extra map[string]any) map[string]any {
		r := map[string]any{
			"ticket_id": "ABC-1", "outcome": string(runner.OutcomePROpened), "settled_at": historyBase.Add(-age),
		}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	rows := map[string]map[string]any{
		"merged by owner": settled(time.Hour, map[string]any{"merged_by_owner_at": historyBase}),
		"merged by a poll": settled(2*time.Hour, map[string]any{
			"rewrite_merged_at": historyBase, "merged_by_owner_at": time.Time{},
		}),
		"open draft": settled(3*time.Hour, map[string]any{
			"wait_reason": "draft", "merged_by_owner_at": time.Time{},
		}),
		"stopped": settled(4*time.Hour, map[string]any{
			"outcome": string(runner.OutcomeBudgetStop), "stop_reason": string(runner.StopAllowanceExhausted),
		}),
		"never settled":      {"ticket_id": "ABC-2", "outcome": string(runner.OutcomeStopped), "settled_at": time.Time{}},
		"oldest":             settled(5*time.Hour, map[string]any{"cancelled_by_owner_at": historyBase}),
		"older than the cap": settled(6*time.Hour, nil),
	}
	var ids []string
	for name, data := range rows {
		id := prefix + "-" + name
		ids = append(ids, id)
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	forget(t, client, ids...)

	got, err := NewRecords(client).RecentSettled(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Run[len(prefix)+1:])
	}
	if want := []string{"merged by owner", "merged by a poll", "open draft", "stopped", "oldest"}; !slices.Equal(names, want) {
		t.Fatalf("history = %v, want %v", names, want)
	}
	wantMerged := []bool{true, true, false, false, false}
	for i, r := range got {
		if r.Merged != wantMerged[i] {
			t.Errorf("%s merged = %t, want %t", names[i], r.Merged, wantMerged[i])
		}
	}
	if !got[4].Cancelled || got[2].Cancelled {
		t.Errorf("cancelled = %t and %t, want the owner's cancel read on the oldest and nowhere else", got[4].Cancelled, got[2].Cancelled)
	}
	if got[2].WaitReason != runner.WaitDraft || got[2].Ticket != "ABC-1" || got[2].Outcome != runner.OutcomePROpened {
		t.Errorf("open draft = %+v, want its ticket, outcome and wait reason", got[2])
	}
	if got[3].Outcome != runner.OutcomeBudgetStop || got[3].StopReason != runner.StopAllowanceExhausted {
		t.Errorf("stopped = %+v, want its outcome and stop reason", got[3])
	}
	if !got[0].SettledAt.Equal(historyBase.Add(-time.Hour)) {
		t.Errorf("newest settled at %v, want %v", got[0].SettledAt, historyBase.Add(-time.Hour))
	}
}

func TestSettledMergedIsTrueOnlyForAMergeThatHappened(t *testing.T) {
	at := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		row  map[string]any
		want bool
	}{
		{"no merge field", map[string]any{}, false},
		{"owner merge time is the zero time", map[string]any{"merged_by_owner_at": time.Time{}}, false},
		{"merged by owner", map[string]any{"merged_by_owner_at": at}, true},
		{"merged measured by a poll", map[string]any{"rewrite_merged_at": at}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := settledMerged(tt.row); got != tt.want {
				t.Errorf("settledMerged = %t, want %t", got, tt.want)
			}
		})
	}
}
