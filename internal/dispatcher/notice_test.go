package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// fakeBreaker reports the breaker tripped, or fails to read it.
type fakeBreaker struct {
	tripped bool
	err     error
}

func (b fakeBreaker) Tripped(context.Context) (bool, error) { return b.tripped, b.err }

// fakeNotices holds pending notices and records what each call did to them.
type fakeNotices struct {
	pending   []Notice
	claimErr  error
	raiseErr  error
	postedErr error
	raised    []Notice
	posted    []string
	unclaimed []string
}

func (n *fakeNotices) Raise(_ context.Context, notice Notice, _ time.Time) error {
	if n.raiseErr != nil {
		return n.raiseErr
	}
	n.raised = append(n.raised, notice)
	return nil
}

func (n *fakeNotices) Claim(context.Context, time.Time, time.Time) ([]Notice, error) {
	claimed := n.pending
	n.pending = nil
	return claimed, n.claimErr
}

func (n *fakeNotices) Posted(_ context.Context, id string, _ time.Time) error {
	if n.postedErr != nil {
		return n.postedErr
	}
	n.posted = append(n.posted, id)
	return nil
}

func (n *fakeNotices) Unclaim(_ context.Context, id string) error {
	n.unclaimed = append(n.unclaimed, id)
	return nil
}

// fakePoster records the notices it posted, failing the ones failFor names.
type fakePoster struct {
	posts   []Notice
	failFor map[string]bool
}

func (p *fakePoster) Post(_ context.Context, n Notice) error {
	if p.failFor[n.ID] {
		return errors.New("slack is unreachable")
	}
	p.posts = append(p.posts, n)
	return nil
}

var identityNotice = Notice{ID: "ABC-1", Class: "identity-mismatch", Link: "https://github.com/o/r/actions/runs/7"}

// noticeDeps is pollDeps with notices pending, a poster, and a count of how
// often the poster was opened.
func noticeDeps(notices *fakeNotices, poster *fakePoster, opens *int) Deps {
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	d.Notices = notices
	d.OpenPoster = func(context.Context) (Poster, error) {
		*opens++
		return poster, nil
	}
	return d
}

func TestPollOpensNoPosterWithNoNoticePending(t *testing.T) {
	opens := 0
	if _, err := Poll(context.Background(), noticeDeps(&fakeNotices{}, &fakePoster{}, &opens), buildConfig); err != nil {
		t.Fatal(err)
	}
	if opens != 0 {
		t.Fatalf("opened the poster %d times with nothing pending", opens)
	}
}

func TestPollPostsAPendingNoticeAndMarksItPosted(t *testing.T) {
	opens := 0
	notices, poster := &fakeNotices{pending: []Notice{identityNotice}}, &fakePoster{}
	if _, err := Poll(context.Background(), noticeDeps(notices, poster, &opens), buildConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(poster.posts, []Notice{identityNotice}) || !slices.Equal(notices.posted, []string{"ABC-1"}) || len(notices.unclaimed) != 0 {
		t.Fatalf("posts %v, posted %v, unclaimed %v", poster.posts, notices.posted, notices.unclaimed)
	}
}

func TestPollUnclaimsANoticeItCouldNotPost(t *testing.T) {
	opens := 0
	other := Notice{ID: "ABC-2", Class: "credential-absent"}
	notices, poster := &fakeNotices{pending: []Notice{identityNotice, other}}, &fakePoster{failFor: map[string]bool{"ABC-1": true}}
	_, err := Poll(context.Background(), noticeDeps(notices, poster, &opens), buildConfig)
	if err == nil {
		t.Fatal("a failed post was not reported")
	}
	if !slices.Equal(notices.unclaimed, []string{"ABC-1"}) || !slices.Equal(notices.posted, []string{"ABC-2"}) {
		t.Fatalf("unclaimed %v, posted %v", notices.unclaimed, notices.posted)
	}
}

func TestPollReportsANoticeItPostedButCouldNotMarkPosted(t *testing.T) {
	opens := 0
	notices, poster := &fakeNotices{pending: []Notice{identityNotice}, postedErr: errFirestore}, &fakePoster{}
	_, err := Poll(context.Background(), noticeDeps(notices, poster, &opens), buildConfig)
	if !errors.Is(err, errFirestore) || len(poster.posts) != 1 || len(notices.unclaimed) != 0 {
		t.Fatalf("err = %v, posts %v, unclaimed %v", err, poster.posts, notices.unclaimed)
	}
}

func TestPollUnclaimsEveryNoticeWhenThePosterCannotOpen(t *testing.T) {
	notices := &fakeNotices{pending: []Notice{identityNotice}}
	d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	d.Notices = notices
	d.OpenPoster = func(context.Context) (Poster, error) { return nil, errors.New("no access to the secret") }
	if _, err := Poll(context.Background(), d, buildConfig); err == nil {
		t.Fatal("an unopened poster was not reported")
	}
	if !slices.Equal(notices.unclaimed, []string{"ABC-1"}) || len(notices.posted) != 0 {
		t.Fatalf("unclaimed %v, posted %v", notices.unclaimed, notices.posted)
	}
}

func TestPollReportsANoticeClaimThatFails(t *testing.T) {
	opens := 0
	_, err := Poll(context.Background(), noticeDeps(&fakeNotices{claimErr: errFirestore}, &fakePoster{}, &opens), buildConfig)
	if !errors.Is(err, errFirestore) || opens != 0 {
		t.Fatalf("err = %v, opens %d", err, opens)
	}
}

func TestPollPostsNoticesWhenLinearCannotBeRead(t *testing.T) {
	opens := 0
	notices, poster := &fakeNotices{pending: []Notice{identityNotice}}, &fakePoster{}
	d := noticeDeps(notices, poster, &opens)
	d.Source = fakeSource{err: errors.New("linear is down")}
	if _, err := Poll(context.Background(), d, buildConfig); err == nil {
		t.Fatal("the Linear failure was dropped")
	}
	if len(poster.posts) != 1 {
		t.Fatalf("posted %v, want the pending notice posted anyway", poster.posts)
	}
}

func TestPollTellsAdmissionWhetherTheBreakerIsTripped(t *testing.T) {
	for name, tt := range map[string]struct {
		breaker fakeBreaker
		want    bool
	}{
		"untripped":                            {fakeBreaker{}, false},
		"tripped":                              {fakeBreaker{tripped: true}, true},
		"the read fails, so it holds the walk": {fakeBreaker{err: errFirestore}, true},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Priority: 1}}}
			d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			d.Breaker = tt.breaker
			if _, err := Poll(context.Background(), d, buildConfig); err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].facts.BreakerTripped != tt.want {
				t.Fatalf("tryClaims = %+v, want BreakerTripped %v", q.tryClaims, tt.want)
			}
		})
	}
}

// fiveHour is a rolling provider window with a $10 limit.
var fiveHour = Window{Name: "5h", Period: 5 * time.Hour, Limit: money.FromUSD(10)}

// deferralPoll is a poll whose one queued run, ABC-18, the 5h window
// withholds, with settled the spend that window holds at the next poll.
func deferralPoll(binding string, settled money.Micros) (*fakeQueue, *fakeNotices, Deps, Config) {
	q := &fakeQueue{
		holds:      map[string]bool{"ABC-18": true},
		candidates: []Candidate{{RunID: "ABC-18", Repo: "octo/scratch", Size: "M", Priority: 1}},
		bindings:   map[string]string{"ABC-18": binding},
		settled:    Settled{Windows: []money.Micros{settled}},
	}
	notices := &fakeNotices{}
	d := pollDeps(fakeSource{issues: []Issue{{ID: "ABC-18", URL: "https://linear.app/w/issue/ABC-18", Body: "ticket body"}}}, q, &fakeWorkflow{})
	d.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: money.FromUSD(1)}}}
	d.Notices = notices
	cfg := Config{Repos: buildConfig.Repos, Budget: BudgetConfig{ProviderWindows: []Window{fiveHour}, Cash: Window{Calendar: true, Limit: money.FromUSD(30)}}}
	return q, notices, d, cfg
}

func TestPollNoticesADeferralWhoseCeilingStillBindsAtTheNextPoll(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(10))
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	want := Notice{ID: "deferral-5h-1790485200", Class: "deferral:5h", Link: "https://linear.app/w/issue/ABC-18"}
	if !slices.Equal(notices.raised, []Notice{want}) {
		t.Fatalf("raised %+v, want %+v", notices.raised, want)
	}
	if len(q.spends) != 1 || !q.spends[0].Equal(pollAt.Add(15*time.Minute)) {
		t.Fatalf("spend read at %v, want the next poll", q.spends)
	}
}

func TestPollRaisesNoNoticeForADeferralThatClearsByTheNextPoll(t *testing.T) {
	_, notices, d, cfg := deferralPoll("5h", money.FromUSD(9))
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 0 {
		t.Fatalf("raised %+v for a ceiling clearing by the next poll", notices.raised)
	}
}

func TestPollReadsNoSpendForAConditionDeferral(t *testing.T) {
	for _, binding := range []string{ConditionRepoBusy, CeilingProviderHalted} {
		q, notices, d, cfg := deferralPoll(binding, money.FromUSD(10))
		if _, err := Poll(context.Background(), d, cfg); err != nil {
			t.Fatal(err)
		}
		if len(q.spends) != 0 || len(notices.raised) != 0 {
			t.Fatalf("%s: spend reads %v, raised %+v", binding, q.spends, notices.raised)
		}
	}
}

func TestPollRaisesNoNoticeForADeferralWithNoIssueLink(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(10))
	d.Source = fakeSource{}
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 0 || len(q.spends) != 0 {
		t.Fatalf("raised %+v, spend reads %v, want nothing without a link", notices.raised, q.spends)
	}
}

func TestPollRaisesNoNoticeWhenTheSpendCannotBeRead(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(10))
	q.spendErr = errFirestore
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 0 {
		t.Fatalf("raised %+v from an unread spend", notices.raised)
	}
}

func TestPollAsksOncePerCeilingWhetherItStillBinds(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(10))
	q.holds["ABC-19"] = true
	q.candidates = append(q.candidates, Candidate{RunID: "ABC-19", Repo: "octo/other", Size: "M", Priority: 2})
	q.bindings["ABC-19"] = "5h"
	d.Source = fakeSource{issues: []Issue{{ID: "ABC-18", URL: "https://linear.app/w/issue/ABC-18"}, {ID: "ABC-19", URL: "https://linear.app/w/issue/ABC-19"}}}
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(q.spends) != 1 || len(notices.raised) != 1 {
		t.Fatalf("spend reads %d, raised %d, want one of each", len(q.spends), len(notices.raised))
	}
}

func TestPollAsksALaterCandidateWhenTheFirstClearsByTheNextPoll(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(9))
	q.holds["ABC-19"] = true
	q.candidates = append(q.candidates, Candidate{RunID: "ABC-19", Repo: "octo/other", Size: "L", Priority: 2})
	q.bindings["ABC-19"] = "5h"
	d.Source = fakeSource{issues: []Issue{{ID: "ABC-18", URL: "https://linear.app/w/issue/ABC-18"}, {ID: "ABC-19", URL: "https://linear.app/w/issue/ABC-19"}}}
	d.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: money.FromUSD(1)}, "L": {ProviderCost: money.FromUSD(2)}}}
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 1 || notices.raised[0].Link != "https://linear.app/w/issue/ABC-19" {
		t.Fatalf("raised %+v, want the larger candidate's notice", notices.raised)
	}
}

func TestPollAsksAgainOnTheSameCeilingAfterARaiseFails(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(10))
	notices.raiseErr = errFirestore
	q.holds["ABC-19"] = true
	q.candidates = append(q.candidates, Candidate{RunID: "ABC-19", Repo: "octo/other", Size: "M", Priority: 2})
	q.bindings["ABC-19"] = "5h"
	d.Source = fakeSource{issues: []Issue{{ID: "ABC-18", URL: "https://linear.app/w/issue/ABC-18"}, {ID: "ABC-19", URL: "https://linear.app/w/issue/ABC-19"}}}
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(q.spends) != 2 {
		t.Fatalf("spend reads %d, want the ceiling asked again after the failed raise", len(q.spends))
	}
}

func TestDeferralNoticeIDEscapesAModelCapsSlash(t *testing.T) {
	got := deferralNoticeID("command-code/deepseek/v4", time.Unix(1788307200, 0))
	if got != "deferral-command-code%2Fdeepseek%2Fv4-1788307200" {
		t.Fatalf("id = %q", got)
	}
}

func TestSlackPostsTheClassAndLinkAlone(t *testing.T) {
	var body map[string]string
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	if err := (Slack{Webhook: srv.URL + "/services/T/B/x", Client: srv.Client()}).Post(context.Background(), identityNotice); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["text"] != "identity-mismatch https://github.com/o/r/actions/runs/7" || contentType != "application/json" {
		t.Fatalf("body %v, content type %q", body, contentType)
	}
}

func TestSlackNeverReportsTheWebhookURL(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid_payload", http.StatusBadRequest)
	}))
	defer refusing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, base := range map[string]string{"a refusal": refusing.URL, "an unreachable host": closed.URL} {
		webhook := base + "/services/T/B/secret-token"
		err := (Slack{Webhook: webhook}).Post(context.Background(), identityNotice)
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("%s: err = %v, want a failure without the webhook URL", name, err)
		}
	}
}

func TestNoticeTextOmitsAMissingLink(t *testing.T) {
	if got := (Notice{Class: "credential-absent"}).Text(); got != "credential-absent" {
		t.Fatalf("text = %q", got)
	}
}

func TestSlackDoesNotEchoAnUnparseableWebhook(t *testing.T) {
	err := (Slack{Webhook: "http://h/\x7fsecret-token"}).Post(context.Background(), identityNotice)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestPollNoticesAFreeTierDeferralAgainstTheFreeTiersOwnBudget(t *testing.T) {
	q, notices, d, cfg := deferralPoll("5h", money.FromUSD(10))
	q.candidates[0].Private, q.reserved = true, Totals{ProviderCost: money.FromUSD(1)}
	q.freeBindings = map[string]string{"ABC-18": CeilingRunnerMinutes}
	d.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: money.FromUSD(1), Minutes: 30}}}
	d.ModelPlans = &fakeModelPlans{plans: map[string]providers.Plan{"p": {PrivateOptIn: true}}}
	cfg.LastResort = []string{"p/free-a", "p/free-b"}
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	if len(notices.raised) != 1 || notices.raised[0].Class != "deferral:"+CeilingRunnerMinutes {
		t.Fatalf("raised %+v, want the runner-minutes ceiling the free tier still meets", notices.raised)
	}
}
