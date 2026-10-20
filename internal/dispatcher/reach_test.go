package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"
)

// fakeReach records what a poll wrote and counts the writes.
type fakeReach struct {
	recorded []RepoReach
	calls    int
	err      error
}

func (f *fakeReach) RecordReach(_ context.Context, reaches []RepoReach) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.recorded = append(f.recorded, reaches...)
	return nil
}

func TestClassifyReach(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		err  error
		want RepoReach
	}{
		{"a read that succeeded", nil, RepoReach{Reachable: true}},
		{"a refused token", &APIError{Status: 401}, RepoReach{Reason: "unauthorized", Status: 401}},
		{"a forbidden read", &APIError{Status: 403}, RepoReach{Reason: "forbidden", Status: 403}},
		{"a repository the token cannot see", fmt.Errorf("wrapped: %w", &APIError{Status: 404}), RepoReach{Reason: "not-found", Status: 404}},
		{"any other status", &APIError{Status: 502}, RepoReach{Reason: "other", Status: 502}},
		{"a network failure", &url.Error{Op: "Get", URL: "https://api.github.com", Err: errors.New("connection reset")}, RepoReach{Reason: "unreachable"}},
		{"a deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), RepoReach{Reason: "unreachable"}},
		{"an unreadable reply", errors.New("decoding the listing"), RepoReach{Reason: "other"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.Repository, tt.want.CheckedAt = "octo/a", at
			if got := classifyReach("octo/a", tt.err, at); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReachRecordNeverCarriesGitHubsResponseText(t *testing.T) {
	err := &APIError{Status: 403, message: "GitHub refused to list: 403 Forbidden: secret-detail"}
	got := classifyReach("octo/a", err, time.Time{})
	if fmt.Sprintf("%+v", got) != "{Repository:octo/a Reachable:false Reason:forbidden Status:403 CheckedAt:0001-01-01 00:00:00 +0000 UTC}" {
		t.Fatalf("record = %+v, want reason and status only", got)
	}
}

func TestReachState(t *testing.T) {
	added := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		record *RepoReach
		want   ReachStatus
	}{
		{"no record yet", nil, "checking"},
		{"a record from before the project was added", &RepoReach{Reachable: true, CheckedAt: added.Add(-time.Minute)}, "checking"},
		{"a read that succeeded since", &RepoReach{Reachable: true, CheckedAt: added.Add(time.Minute)}, "reachable"},
		{"a read that failed since", &RepoReach{Reason: "forbidden", CheckedAt: added.Add(time.Minute)}, "cannot-read"},
		{"a read at the moment it was added", &RepoReach{Reachable: true, CheckedAt: added}, "reachable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReachState(tt.record, added); got != tt.want {
				t.Fatalf("state = %q, want %q", got, tt.want)
			}
		})
	}
}
