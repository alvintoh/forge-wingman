package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/planrefresh"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// recordingPlans is a plan store recording every call made on it. A refresh is
// handed only this interface, which holds no method that writes a plan, so the
// calls it records are the whole of what a refresh can do to the store.
type recordingPlans struct {
	rows  []providers.Listing
	facts map[string]providers.Facts
	calls []string
}

func (p *recordingPlans) List(context.Context) ([]providers.Listing, error) {
	p.calls = append(p.calls, "list")
	return p.rows, nil
}

func (p *recordingPlans) LatestFacts(_ context.Context, provider string) (providers.Facts, string, bool, error) {
	p.calls = append(p.calls, "latest "+provider)
	facts, ok := p.facts[provider]
	return facts, facts.ReadOn, ok, nil
}

func (p *recordingPlans) PutFacts(_ context.Context, provider, date string, facts providers.Facts) error {
	p.calls = append(p.calls, "put "+provider+" "+date)
	if p.facts == nil {
		p.facts = map[string]providers.Facts{}
	}
	p.facts[provider] = facts
	return nil
}

// planSources serves one plan's page, model list, leaderboard and free feed, so
// that a refresh has all four to read.
func planSources(t *testing.T) string {
	t.Helper()
	return listSources(t, map[string]string{"": `{"data":[{"id":"glm-5"}]}`})
}

// listSources serves planSources' page, leaderboard and feed, and each plan's own
// model list at /<plan>/models from lists, keyed by the plan's name; the key ""
// answers /models.
func listSources(t *testing.T, lists map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if plan, ok := strings.CutSuffix(r.URL.Path, "/models"); ok {
			_, _ = io.WriteString(w, lists[strings.TrimPrefix(plan, "/")])
			return
		}
		switch r.URL.Path {
		case "/leaderboard":
			_, _ = io.WriteString(w, `{"benchmark":"Coding","date":"2026-10-12","models":[`+
				`{"model":"glm-5","score":61.2,"provenance":"independent","margin":"±1.8"}]}`)
		case "/feed":
			_, _ = io.WriteString(w, `{"plans":[{"provider":"configured","price_usd":15,`+
				`"limit_behaviour":"hard-stop","models":["glm-5"],"perks":["No training on your data"]}]}`)
		default:
			_, _ = io.WriteString(w, `<html><body><div id="plans">GOAT $15 a month</div></body></html>`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// refresherFor builds the refresher a refresh runs with, against base, at the
// fixed clock the read dates are asserted from.
func refresherFor(t *testing.T, base string) planrefresh.Refresher {
	t.Helper()
	r := planrefresh.Refresher{
		Vendor: planrefresh.Vendor{HTTP: http.DefaultClient, Base: func(string) string { return base }},
		Now:    func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) },
	}
	var err error
	r.Board, err = planrefresh.Board{Endpoint: base + "/leaderboard", HTTP: http.DefaultClient}.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.Feed, err = planrefresh.Feed{Endpoint: base + "/feed", HTTP: http.DefaultClient}.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// configuredPlan is one plan's listing, defined with billing so a refresh reads it.
func configuredPlan(base string) providers.Listing {
	return providers.Listing{Provider: "configured", Plan: providers.Plan{Definition: providers.Definition{
		Name:           "GOAT",
		MonthlyPrice:   15 * money.Dollar,
		Pages:          []providers.Locator{{URL: base + "/pricing", Selector: "#plans"}},
		LimitBehaviour: providers.LimitHardStop,
		Billing:        providers.BillingPerToken,
	}}}
}

// TestRefreshPlansWritesOneFactsDocumentPerConfiguredPlan asserts the refresh
// writes each configured plan's own facts under the read's date, skips a plan
// with no billing recorded, and calls nothing else on the store.
func TestRefreshPlansWritesOneFactsDocumentPerConfiguredPlan(t *testing.T) {
	base := planSources(t)
	plans := &recordingPlans{rows: []providers.Listing{
		configuredPlan(base),
		{Provider: "unconfigured", Plan: providers.Plan{Definition: providers.Definition{Name: "unused"}}},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := refreshPlans(context.Background(), logger, plans, refresherFor(t, base), ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"list", "latest configured", "put configured 2026-10-14"}
	if !slices.Equal(plans.calls, want) {
		t.Fatalf("calls %v, want exactly %v", plans.calls, want)
	}
	got := plans.facts["configured"]
	if got.ReadOn != "2026-10-14" || len(got.Pages) != 1 || got.Pages[0].Text != "GOAT $15 a month" {
		t.Fatalf("wrote %+v, want the plan's own facts under the read's date", got)
	}
	if _, written := plans.facts["unconfigured"]; written {
		t.Fatal("wrote facts for a plan with no billing recorded")
	}
}

// TestRefreshPlansRefusesAPlanTheStoreDoesNotHold asserts a refresh asked for one
// plan says so, rather than succeeding having written nothing.
func TestRefreshPlansRefusesAPlanTheStoreDoesNotHold(t *testing.T) {
	plans := &recordingPlans{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := refreshPlans(context.Background(), logger, plans, planrefresh.Refresher{}, "missing")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want the plan named as one the store does not hold", err)
	}
	if !slices.Equal(plans.calls, []string{"list"}) {
		t.Fatalf("calls %v, want the listing alone", plans.calls)
	}
}

// failingPut is a plan store whose write for one provider fails.
type failingPut struct {
	*recordingPlans
	fail string
}

func (p failingPut) PutFacts(ctx context.Context, provider, date string, facts providers.Facts) error {
	if provider == p.fail {
		return errors.New("write refused")
	}
	return p.recordingPlans.PutFacts(ctx, provider, date, facts)
}

// TestRefreshPlansContinuesPastAPlanWhoseWriteFails asserts one plan's failed
// write is logged and the next plan is still refreshed, and that the run still
// ends in an error naming the plan that failed.
func TestRefreshPlansContinuesPastAPlanWhoseWriteFails(t *testing.T) {
	base := planSources(t)
	second := configuredPlan(base)
	second.Provider = "second"
	inner := &recordingPlans{rows: []providers.Listing{configuredPlan(base), second}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := refreshPlans(context.Background(), logger, failingPut{inner, "configured"}, refresherFor(t, base), "")
	if err == nil || !strings.Contains(err.Error(), "configured") || strings.Contains(err.Error(), "second") {
		t.Fatalf("err = %v, want only the plan whose write failed named", err)
	}
	if _, written := inner.facts["second"]; !written {
		t.Fatal("the plan after the failed one was not refreshed")
	}
}

// modelList is a models endpoint's reply naming ids, in order.
func modelList(ids ...string) string {
	data := make([]string, len(ids))
	for i, id := range ids {
		data[i] = strconv.Quote(id)
	}
	return `{"data":[{"id":` + strings.Join(data, `},{"id":`) + `}]}`
}

// lastGoodList is the facts a plan last recorded: a good read of four models on
// 2026-10-07, a week before refresherFor's clock.
func lastGoodList() providers.Facts {
	facts := providers.Facts{ReadOn: "2026-10-07", Models: providers.ModelsFacts{
		Source: providers.Source{State: providers.ReadUnchanged, LastGoodReadOn: "2026-10-07"},
	}}
	for _, id := range []string{"glm-5", "glm-6", "glm-7", "glm-8"} {
		facts.Models.Models = append(facts.Models.Models, providers.ModelFacts{Model: id})
	}
	return facts
}

// TestRefreshPlansRefusesAPartialOrMalformedModelList asserts a model list that
// is missing a rate-carded model, holds an id that is not a model id, is over
// the cap or drops more than the allowed share of the last good list is recorded
// as could-not-read naming why, beside the last good list and its date, and that
// the refresh fails naming the plan while still refreshing the plan beside it,
// whose list dropping exactly the allowed share is recorded as changed today.
func TestRefreshPlansRefusesAPartialOrMalformedModelList(t *testing.T) {
	overCap := []string{"glm-5", "glm-6", "glm-7", "glm-8"}
	for i := len(overCap); i <= 500; i++ {
		overCap = append(overCap, "m-"+strconv.Itoa(i))
	}
	for _, tt := range []struct {
		name  string
		list  string
		names []string
	}{
		{"missing a rate-carded model", modelList("glm-6", "glm-7", "glm-8"), []string{"lacks glm-5"}},
		{"holding a word that is not a model id", modelList("glm-5", "glm-6", "glm-7", "glm-8", "New"), []string{`"New"`}},
		{"holding an id with a space", modelList("glm-5", "glm-6", "glm-7", "glm-8", "glm 9"), []string{`"glm 9"`}},
		{"over the cap", modelList(overCap...), []string{"501 models"}},
		{"dropping more than the share", modelList("glm-5", "glm-6"), []string{"drops 2", "4 models"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := listSources(t, map[string]string{
				"configured": tt.list,
				"second":     modelList("glm-5", "glm-6", "glm-7"),
			})
			second := configuredPlan(base)
			second.Provider = "second"
			plans := &recordingPlans{
				rows:  []providers.Listing{configuredPlan(base), second},
				facts: map[string]providers.Facts{"configured": lastGoodList(), "second": lastGoodList()},
			}
			refresher := refresherFor(t, base)
			refresher.Vendor.Base = func(plan string) string { return base + "/" + plan }
			refresher.RateCard = func(string) []string { return []string{"glm-5"} }
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))

			err := refreshPlans(context.Background(), logger, plans, refresher, "")
			if err == nil || !strings.Contains(err.Error(), "configured") || strings.Contains(err.Error(), "second") {
				t.Fatalf("err = %v, want only the plan whose list was refused named", err)
			}
			got := plans.facts["configured"].Models
			if got.Source.State != providers.ReadCouldNotRead || got.Source.LastGoodReadOn != "2026-10-07" {
				t.Fatalf("model list source is %+v, want could-not-read with the last good date", got.Source)
			}
			for _, name := range tt.names {
				if !strings.Contains(got.Source.Note, name) || !strings.Contains(err.Error(), name) {
					t.Fatalf("note %q and err %v, want each naming %s", got.Source.Note, err, name)
				}
			}
			ids := make([]string, len(got.Models))
			for i, m := range got.Models {
				ids[i] = m.Model
			}
			if want := []string{"glm-5", "glm-6", "glm-7", "glm-8"}; !slices.Equal(ids, want) {
				t.Fatalf("models are %v, want the last good list %v kept", ids, want)
			}
			kept := plans.facts["second"].Models.Source
			if kept.State != providers.ReadChanged || kept.LastGoodReadOn != "2026-10-14" ||
				!slices.Equal(kept.Changes, []string{"dropped model glm-8"}) {
				t.Fatalf("the other plan's list is %+v, want it recorded as changed today", kept)
			}
		})
	}
}

// six is the last good list the repeat tests start from, and retired the same
// list after the vendor retires two of its models: a drop of a third of it.
var (
	six     = []string{"glm-1", "glm-2", "glm-3", "glm-4", "glm-5", "glm-6"}
	retired = six[:4]
)

// weekly refreshes plan "configured" once a week from 2026-10-14, its model list
// answering with the week's own reply, starting from a good read of six on
// 2026-10-07 with glm-1 on the rate card. It returns each week's error and the
// facts each week wrote.
func weekly(t *testing.T, replies ...string) ([]error, []providers.Facts) {
	t.Helper()
	days := make([]int, len(replies))
	for i := range days {
		days[i] = 7 * i
	}
	return reads(t, days, []string{"glm-1"}, replies)
}

// reads is weekly with each read days[i] days after 2026-10-14 and card as the
// plan's rate card.
func reads(t *testing.T, days []int, card, replies []string) ([]error, []providers.Facts) {
	t.Helper()
	last := providers.Facts{ReadOn: "2026-10-07", Models: providers.ModelsFacts{
		Source: providers.Source{State: providers.ReadUnchanged, LastGoodReadOn: "2026-10-07"},
	}}
	for _, id := range six {
		last.Models.Models = append(last.Models.Models, providers.ModelFacts{Model: id})
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var errs []error
	var written []providers.Facts
	for i, reply := range replies {
		base := listSources(t, map[string]string{"configured": reply})
		plans := &recordingPlans{
			rows:  []providers.Listing{configuredPlan(base)},
			facts: map[string]providers.Facts{"configured": last},
		}
		refresher := refresherFor(t, base)
		refresher.Vendor.Base = func(plan string) string { return base + "/" + plan }
		refresher.RateCard = func(string) []string { return card }
		refresher.Now = func() time.Time { return time.Date(2026, 10, 14+days[i], 12, 0, 0, 0, time.UTC) }
		errs = append(errs, refreshPlans(context.Background(), logger, plans, refresher, ""))
		last = plans.facts["configured"]
		written = append(written, last)
	}
	return errs, written
}

// ids is the model ids a facts document's list records, in order.
func ids(facts providers.Facts) []string {
	out := make([]string, len(facts.Models.Models))
	for i, m := range facts.Models.Models {
		out[i] = m.Model
	}
	return out
}

// TestRefreshPlansAcceptsADropRefusedForTwoReadsRunning asserts a list refused
// for dropping too many models is refused the first week, keeping the last good
// list, and accepted the second week it reads the same, as the new last good
// list with a note naming the repeat, and then reads as unchanged.
func TestRefreshPlansAcceptsADropRefusedForTwoReadsRunning(t *testing.T) {
	errs, weeks := weekly(t, modelList(retired...), modelList(retired...), modelList(retired...))

	first := weeks[0].Models
	if errs[0] == nil || first.Source.State != providers.ReadCouldNotRead || first.Source.LastGoodReadOn != "2026-10-07" ||
		!slices.Equal(ids(weeks[0]), six) {
		t.Fatalf("week 1: err %v, models %+v, want the drop refused and the last good list kept", errs[0], first)
	}
	if !slices.Equal(first.Refused.Models, retired) || first.Refused.FirstOn != "2026-10-14" {
		t.Fatalf("week 1 remembers %+v, want the refused list with the date it was first refused", first.Refused)
	}

	second := weeks[1].Models
	if errs[1] != nil {
		t.Fatalf("week 2: err %v, want the repeated list accepted", errs[1])
	}
	if second.Source.State != providers.ReadChanged || second.Source.LastGoodReadOn != "2026-10-21" ||
		!slices.Equal(ids(weeks[1]), retired) || !strings.Contains(second.Source.Note, "2 dates running") {
		t.Fatalf("week 2 models %+v, want the list accepted today with a note naming the repeat", second)
	}
	if len(second.Refused.Models) != 0 || second.Refused.FirstOn != "" {
		t.Fatalf("week 2 remembers %+v, want the refusal cleared", second.Refused)
	}

	if third := weeks[2].Models.Source; errs[2] != nil || third.State != providers.ReadUnchanged || third.Note != "" {
		t.Fatalf("week 3: err %v, source %+v, want the new list unchanged", errs[2], third)
	}
}

// TestRefreshPlansAcceptsOnlyTheSameDropReadRunning asserts a refused drop is
// accepted only when the same list repeats it on the next read on a later date,
// a failed read between the two leaving the count as it was; and that an empty
// list, or one over the cap, holding a malformed id or missing a rate-carded
// model, is never accepted.
func TestRefreshPlansAcceptsOnlyTheSameDropReadRunning(t *testing.T) {
	overCap := slices.Clone(six)
	for i := len(overCap); i <= 500; i++ {
		overCap = append(overCap, "m-"+strconv.Itoa(i))
	}
	malformed := modelList(append(slices.Clone(six), "New")...)
	unrated := modelList(six[1:]...)
	for _, tt := range []struct {
		name     string
		replies  []string
		accepted bool
		days     []int
		noCard   bool
	}{
		{name: "the same drop twice on one date", replies: []string{modelList(retired...), modelList(retired...)}, days: []int{0, 0}},
		{name: "the same drop on one date, then a later one", replies: []string{modelList(retired...), modelList(retired...), modelList(retired...)},
			accepted: true, days: []int{0, 0, 1}},
		{name: "an empty list twice", replies: []string{`{"data":[]}`, `{"data":[]}`}, noCard: true},
		{name: "the same drop twice", replies: []string{modelList(retired...), modelList(retired...)}, accepted: true},
		{name: "the same drop reordered", replies: []string{modelList(retired...), modelList("glm-4", "glm-3", "glm-2", "glm-1")}, accepted: true},
		{name: "a failed read between", replies: []string{modelList(retired...), "", modelList(retired...)}, accepted: true},
		{name: "a changed drop", replies: []string{modelList(retired...), modelList(six[:3]...)}, accepted: false},
		{name: "another refusal between", replies: []string{modelList(retired...), unrated, modelList(retired...)}, accepted: false},
		{name: "a malformed list twice", replies: []string{malformed, malformed}, accepted: false},
		{name: "an over-cap list twice", replies: []string{modelList(overCap...), modelList(overCap...)}, accepted: false},
		{name: "a list missing a rate-carded model twice", replies: []string{unrated, unrated}, accepted: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			days := tt.days
			if days == nil {
				for i := range tt.replies {
					days = append(days, 7*i)
				}
			}
			card := []string{"glm-1"}
			if tt.noCard {
				card = nil
			}
			errs, weeks := reads(t, days, card, tt.replies)
			err, last := errs[len(errs)-1], weeks[len(weeks)-1]
			if tt.accepted && (err != nil || last.Models.Source.State != providers.ReadChanged) {
				t.Fatalf("err %v, models %+v, want the list accepted", err, last.Models)
			}
			if !tt.accepted && (err == nil || !slices.Equal(ids(last), six)) {
				t.Fatalf("err %v, models %v, want the list refused and the last good list kept", err, ids(last))
			}
		})
	}
}
