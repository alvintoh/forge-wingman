package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// storedNotices keeps notices as the store does: a raise creates one only when
// its id is absent, a claim takes the pending ones, and a clear removes one.
type storedNotices struct {
	notices  map[string]Notice
	states   map[string]string
	raiseErr error
	clearErr error
	raised   []string
	cleared  []string
}

func newStoredNotices() *storedNotices {
	return &storedNotices{notices: map[string]Notice{}, states: map[string]string{}}
}

func (s *storedNotices) Raise(_ context.Context, n Notice, _ time.Time) error {
	if s.raiseErr != nil {
		return s.raiseErr
	}
	s.raised = append(s.raised, n.ID)
	if _, ok := s.notices[n.ID]; !ok {
		s.notices[n.ID], s.states[n.ID] = n, "pending"
	}
	return nil
}

func (s *storedNotices) Claim(context.Context, time.Time, time.Time) ([]Notice, error) {
	var claimed []Notice
	for id, state := range s.states {
		if state == "pending" {
			s.states[id] = "claimed"
			claimed = append(claimed, s.notices[id])
		}
	}
	return claimed, nil
}

func (s *storedNotices) Posted(_ context.Context, id string, _ time.Time) error {
	s.states[id] = "posted"
	return nil
}

func (s *storedNotices) Unclaim(_ context.Context, id string) error {
	s.states[id] = "pending"
	return nil
}

func (s *storedNotices) Clear(_ context.Context, id string) error {
	if s.clearErr != nil {
		return s.clearErr
	}
	s.cleared = append(s.cleared, id)
	delete(s.notices, id)
	delete(s.states, id)
	return nil
}

// fakeRulesetMarks remembers the repositories marked as requiring the merge checks.
type fakeRulesetMarks struct {
	seen    map[string]bool
	markErr error
	seenErr error
}

func newFakeRulesetMarks() *fakeRulesetMarks { return &fakeRulesetMarks{seen: map[string]bool{}} }

func (m *fakeRulesetMarks) MarkRequired(_ context.Context, repo string, _ time.Time) error {
	if m.markErr != nil {
		return m.markErr
	}
	m.seen[repo] = true
	return nil
}

func (m *fakeRulesetMarks) SeenRequired(_ context.Context, repo string) (bool, error) {
	return m.seen[repo], m.seenErr
}

const rulesetRepo = "octo/scratch"

var rulesetConfig = Config{Repos: []string{rulesetRepo}}

// rulesetStore is the notices and marks a run of polls shares.
type rulesetStore struct {
	notices *storedNotices
	marks   *fakeRulesetMarks
}

func newRulesetStore() rulesetStore {
	return rulesetStore{notices: newStoredNotices(), marks: newFakeRulesetMarks()}
}

// rulesetPoll is one poll over rulesetRepo whose rules answer required and
// readErr, returning what it posted, its debug log, and its error.
func rulesetPoll(t *testing.T, st rulesetStore, required []string, readErr error) ([]Notice, string, error) {
	t.Helper()
	poster := &fakePoster{}
	var logs bytes.Buffer
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	d.Notices = st.notices
	d.RulesetMarks = st.marks
	d.OpenPoster = func(context.Context) (Poster, error) { return poster, nil }
	d.Checks.GitHub = &fakeChecksGitHub{required: required, requiredErr: readErr}
	d.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	_, err := Poll(context.Background(), d, rulesetConfig)
	return poster.posts, logs.String(), err
}

// rulesetStep is one poll of a run and how many notices it must post.
type rulesetStep struct {
	name     string
	required []string
	readErr  error
	posts    int
}

func runRulesetSteps(t *testing.T, st rulesetStore, steps []rulesetStep) [][]Notice {
	t.Helper()
	var all [][]Notice
	for _, step := range steps {
		posts, _, err := rulesetPoll(t, st, step.required, step.readErr)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if len(posts) != step.posts {
			t.Fatalf("%s: posted %+v, want %d notice(s)", step.name, posts, step.posts)
		}
		all = append(all, posts)
	}
	return all
}

func TestPollNoticesAMissingMergeCheckOnceAndAgainOnlyAfterARestore(t *testing.T) {
	posts := runRulesetSteps(t, newRulesetStore(), []rulesetStep{
		{name: "required", required: []string{"go", "web"}},
		{name: "web dropped", required: []string{"go"}, posts: 1},
		{name: "still dropped", required: []string{"go"}},
		{name: "restored", required: []string{"web", "go"}},
		{name: "dropped again", required: []string{"go", "lint"}, posts: 1},
	})
	want := Notice{
		ID:     "ruleset-octo%2Fscratch",
		Class:  "ruleset:octo/scratch",
		Detail: "main's rules no longer require the web check, so an auto-merge would not wait for it",
		Link:   "https://github.com/octo/scratch/settings/rules",
	}
	for _, i := range []int{1, 4} {
		if posts[i][0] != want {
			t.Fatalf("poll %d posted %+v, want %+v", i, posts[i][0], want)
		}
	}
}

// Rules no poll has seen require the merge checks never required them, so a
// repository whose CI names its checks otherwise is not noticed.
func TestPollPostsNothingForRulesNeverSeenToRequireTheMergeChecks(t *testing.T) {
	st := newRulesetStore()
	posts, logs, err := rulesetPoll(t, st, nil, fmt.Errorf("octo/scratch@main: %w", ErrNoRequiredChecks))
	if err != nil || len(posts) != 0 || len(st.notices.raised) != 0 {
		t.Fatalf("err = %v, posted %+v, raised %v, want nothing", err, posts, st.notices.raised)
	}
	if !strings.Contains(logs, "level=DEBUG msg=rulesetOutOfScope repo=octo/scratch") {
		t.Fatalf("log lacks rulesetOutOfScope:\n%s", logs)
	}
}

func TestPollNamesBothChecksWhenTheRulesRequireNone(t *testing.T) {
	posts := runRulesetSteps(t, newRulesetStore(), []rulesetStep{
		{name: "required", required: []string{"go", "web"}},
		{name: "none required", readErr: fmt.Errorf("octo/scratch@main: %w", ErrNoRequiredChecks), posts: 1},
	})
	if got := posts[1][0].Detail; got != "main's rules no longer require the go and web checks, so an auto-merge would not wait for them" {
		t.Fatalf("detail = %q, want both checks named", got)
	}
}

func TestPollPostsNothingWhileBothMergeChecksAreRequired(t *testing.T) {
	st := newRulesetStore()
	posts, _, err := rulesetPoll(t, st, []string{"lint", "web", "go"}, nil)
	if err != nil || len(posts) != 0 || len(st.notices.raised) != 0 {
		t.Fatalf("err = %v, posted %+v, raised %v, want nothing", err, posts, st.notices.raised)
	}
	if !st.marks.seen[rulesetRepo] {
		t.Fatal("rules requiring both checks were not marked")
	}
}

func TestPollThatCannotReadTheRulesPostsNothingAndLogsTheError(t *testing.T) {
	for _, tt := range []struct {
		name, deadline string
		err            error
	}{
		{"refused", "false", errors.New("reading the rules: 502 Bad Gateway")},
		{"timed out", "true", fmt.Errorf("reading the rules: %w", context.DeadlineExceeded)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newRulesetStore()
			st.marks.seen[rulesetRepo] = true
			posts, logs, err := rulesetPoll(t, st, nil, tt.err)
			if err != nil {
				t.Fatal(err)
			}
			if len(posts) != 0 || len(st.notices.raised) != 0 || len(st.notices.cleared) != 0 {
				t.Fatalf("posted %+v, raised %v, cleared %v, want nothing", posts, st.notices.raised, st.notices.cleared)
			}
			line := "msg=rulesetUnread repo=octo/scratch branch=main deadline=" + tt.deadline + ` err="` + tt.err.Error() + `"`
			if !strings.Contains(logs, line) {
				t.Fatalf("log lacks %q:\n%s", line, logs)
			}
		})
	}
}

// An unread poll in the middle of a drift does not count as a restore, so the
// drift is not noticed a second time when the rules read again.
func TestAnUnreadPollLeavesAnOpenDriftNoticed(t *testing.T) {
	runRulesetSteps(t, newRulesetStore(), []rulesetStep{
		{name: "required", required: []string{"go", "web"}},
		{name: "dropped", required: []string{"go"}, posts: 1},
		{name: "unread", readErr: errors.New("reading the rules: timeout")},
		{name: "still dropped", required: []string{"go"}},
	})
}

func TestPollReportsARulesetStoreFailure(t *testing.T) {
	for _, tt := range []struct {
		name     string
		required []string
		fail     func(rulesetStore)
		event    string
	}{
		{"raise", []string{"go"}, func(st rulesetStore) { st.marks.seen[rulesetRepo] = true; st.notices.raiseErr = errFirestore }, "rulesetNoticeNotRaised"},
		{"clear", []string{"go", "web"}, func(st rulesetStore) { st.notices.clearErr = errFirestore }, "rulesetNoticeNotCleared"},
		{"mark", []string{"go", "web"}, func(st rulesetStore) { st.marks.markErr = errFirestore }, "rulesetMarkNotWritten"},
		{"mark read", []string{"go"}, func(st rulesetStore) { st.marks.seenErr = errFirestore }, "rulesetMarkUnread"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newRulesetStore()
			tt.fail(st)
			posts, logs, err := rulesetPoll(t, st, tt.required, nil)
			if !errors.Is(err, errFirestore) || !strings.Contains(logs, "msg="+tt.event) || len(posts) != 0 {
				t.Fatalf("err = %v, posted %+v, want %v and %s logged:\n%s", err, posts, errFirestore, tt.event, logs)
			}
		})
	}
}
