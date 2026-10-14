package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"glm-5"}]}`)
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
