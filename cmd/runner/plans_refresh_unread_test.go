package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/planrefresh"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// The dates the unread tests age a source from, against refresherFor's clock of
// 2026-10-14: 21 days before it, and 22.
const (
	twentyOneDaysAgo = "2026-09-23"
	twentyTwoDaysAgo = "2026-09-22"
)

// unreadKind is one plan-owned source and how to make a plan's read of it fail
// and its last document record it.
type unreadKind struct {
	name string
	// plan returns the plan's listing with this source made unreadable.
	plan func(providers.Listing) providers.Listing
	// list is the plan's model list reply.
	list string
	// last is the facts last recorded, this source recorded with src.
	last func(src providers.Source) providers.Facts
}

// unreadKinds are the two sources a plan owns. A page whose selector the page
// does not hold, and a model list that is not JSON, are each unread.
var unreadKinds = []unreadKind{
	{
		name: planrefresh.SourceVendorPage,
		plan: func(l providers.Listing) providers.Listing {
			l.Plan.Definition.Pages[0].Selector = "#missing"
			return l
		},
		list: modelList("glm-5"),
		last: func(src providers.Source) providers.Facts {
			return providers.Facts{ReadOn: "2026-10-07", Pages: []providers.PageFacts{{
				URL: "http://user:secret@BASE/pricing", Selector: "#missing", Source: src}}}
		},
	},
	{
		name: planrefresh.SourceModelList,
		plan: func(l providers.Listing) providers.Listing { return l },
		list: "",
		last: func(src providers.Source) providers.Facts {
			return providers.Facts{ReadOn: "2026-10-07", Models: providers.ModelsFacts{Source: src}}
		},
	},
}

// credentialed is plan's listing with the credentials a vendor address may carry
// in its page's URL, which nothing logged or returned may repeat. The address
// names the host BASE, which unreadRun replaces with the server's own.
func credentialed(base string, plan string) providers.Listing {
	l := configuredPlan(base)
	l.Provider = plan
	l.Plan.Definition.Pages = []providers.Locator{{URL: strings.Replace(base, "http://", "http://user:secret@", 1) + "/pricing", Selector: "#plans"}}
	return l
}

// unreadRun refreshes plans, each served its own model list from lists, at
// refresherFor's clock, logging as JSON.
func unreadRun(t *testing.T, rows []providers.Listing, last map[string]providers.Facts, lists map[string]string, tweak func(*planrefresh.Refresher)) (*recordingPlans, []map[string]any, error) {
	t.Helper()
	base := listSources(t, lists)
	host := strings.TrimPrefix(base, "http://")
	for i := range rows {
		pages := rows[i].Plan.Definition.Pages
		pages[0].URL = strings.Replace(pages[0].URL, "BASE", host, 1)
	}
	for _, facts := range last {
		for i := range facts.Pages {
			facts.Pages[i].URL = strings.Replace(facts.Pages[i].URL, "BASE", host, 1)
		}
	}
	plans := &recordingPlans{rows: rows, facts: last}
	refresher := refresherFor(t, base)
	refresher.Vendor.Base = func(plan string) string { return base + "/" + plan }
	if tweak != nil {
		tweak(&refresher)
	}
	var logged bytes.Buffer
	err := refreshPlans(context.Background(), slog.New(slog.NewJSONHandler(&logged, nil)), plans, refresher, "")
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logged.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		records = append(records, record)
	}
	return plans, records, err
}

// unreadPlan is plan "configured" with kind's source made unreadable and a
// credentialed page address, and its last facts, if any, naming src for that source.
func unreadPlan(t *testing.T, kind unreadKind, src *providers.Source) (rows []providers.Listing, last map[string]providers.Facts, lists map[string]string) {
	t.Helper()
	row := kind.plan(credentialed("http://BASE", "configured"))
	if src != nil {
		last = map[string]providers.Facts{"configured": kind.last(*src)}
	}
	return []providers.Listing{row}, last, map[string]string{"configured": kind.list}
}

// withEvent returns the log records that are msg.
func withEvent(records []map[string]any, msg string) []map[string]any {
	return slices.DeleteFunc(slices.Clone(records), func(r map[string]any) bool { return r["msg"] != msg })
}

// TestRefreshPlansLogsEachSourceItCannotRead asserts a vendor page and a model
// list that cannot be read each log plansRefreshSourceUnread once, naming the
// plan and the source as fields of their own, never the page's address, and that
// the run stays green and records the date the source was first found unreadable.
func TestRefreshPlansLogsEachSourceItCannotRead(t *testing.T) {
	for _, kind := range unreadKinds {
		t.Run(kind.name, func(t *testing.T) {
			rows, last, lists := unreadPlan(t, kind, nil)
			plans, records, err := unreadRun(t, rows, last, lists, nil)
			if err != nil {
				t.Fatalf("err = %v, want the first read of an unread source green", err)
			}
			unread := withEvent(records, "plansRefreshSourceUnread")
			if len(unread) != 1 || unread[0]["provider"] != "configured" || unread[0]["source"] != kind.name {
				t.Fatalf("unread records %v, want one naming provider configured and source %s", unread, kind.name)
			}
			wantHost := ""
			if kind.name == planrefresh.SourceVendorPage {
				wantHost = "127.0.0.1"
			}
			if host, _ := unread[0]["host"].(string); host != wantHost || unread[0]["days_unread"] != float64(0) {
				t.Fatalf("record %v, want host %q (no port, a page only) and 0 days unread", unread[0], wantHost)
			}
			if out, _ := json.Marshal(records); strings.Contains(string(out), "secret") {
				t.Fatalf("log %s repeats the page's credentials", out)
			}
			facts := plans.facts["configured"]
			got := facts.Models.Source
			if kind.name == planrefresh.SourceVendorPage {
				got = facts.Pages[0].Source
			}
			if got.FirstUnreadOn != "2026-10-14" || got.LastGoodReadOn != "" {
				t.Fatalf("source = %+v, want it first found unreadable today with no good read", got)
			}
		})
	}
}

// TestRefreshPlansFailsASourceUnreadForMoreThan21Days asserts a source last read
// well, or never read well and first found unreadable, 21 days ago leaves the run
// green and 22 days ago fails it naming the plan and the source, with the plan's
// facts still written and the page's credentials nowhere in the error.
func TestRefreshPlansFailsASourceUnreadForMoreThan21Days(t *testing.T) {
	for _, kind := range unreadKinds {
		for _, never := range []bool{false, true} {
			for _, tt := range []struct {
				since string
				days  int
				red   bool
			}{{twentyOneDaysAgo, 21, false}, {twentyTwoDaysAgo, 22, true}} {
				name := kind.name + " last read " + tt.since
				src := providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: tt.since}
				if never {
					name = kind.name + " first unread " + tt.since
					src = providers.Source{State: providers.ReadCouldNotRead, FirstUnreadOn: tt.since}
				}
				t.Run(name, func(t *testing.T) {
					rows, last, lists := unreadPlan(t, kind, &src)
					plans, records, err := unreadRun(t, rows, last, lists, nil)
					unread := withEvent(records, "plansRefreshSourceUnread")
					if len(unread) != 1 || unread[0]["days_unread"] != float64(tt.days) {
						t.Fatalf("unread records %v, want one stating %d days unread", unread, tt.days)
					}
					if !tt.red {
						if err != nil {
							t.Fatalf("err = %v, want a source unread for 21 days green", err)
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), "plan configured") || !strings.Contains(err.Error(), kind.name) ||
						!strings.Contains(err.Error(), tt.since) || strings.Contains(err.Error(), "secret") {
						t.Fatalf("err = %v, want the plan, the source and its date named without the page's credentials", err)
					}
					if !slices.Contains(plans.calls, "put configured 2026-10-14") {
						t.Fatalf("calls %v, want the plan's facts written before the run fails", plans.calls)
					}
				})
			}
		}
	}
}

// TestRefreshPlansFailsOnlyThePlanWhoseSourceIsStale asserts a stale source on
// one plan fails the run naming that plan alone, after the plan beside it is
// still refreshed and written.
func TestRefreshPlansFailsOnlyThePlanWhoseSourceIsStale(t *testing.T) {
	stale := providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: twentyTwoDaysAgo}
	rows, last, lists := unreadPlan(t, unreadKinds[1], &stale)
	rows = append(rows, credentialed("http://BASE", "second"))
	lists["second"] = modelList("glm-5")
	plans, _, err := unreadRun(t, rows, last, lists, nil)
	if err == nil || !strings.Contains(err.Error(), "plan configured") || strings.Contains(err.Error(), "second") {
		t.Fatalf("err = %v, want only the stale plan named", err)
	}
	second, written := plans.facts["second"]
	if !written || !second.Models.Source.ReadOK() {
		t.Fatalf("second plan's facts %+v written %v, want it refreshed and read", second, written)
	}
}

// TestRefreshPlansFailsASharedSourceOnceForEveryPlan asserts a leaderboard and a
// free feed unread for 22 days each fail the run once however many plans share
// them, and neither does at 21 days.
func TestRefreshPlansFailsASharedSourceOnceForEveryPlan(t *testing.T) {
	for _, tt := range []struct {
		since string
		red   bool
	}{{twentyOneDaysAgo, false}, {twentyTwoDaysAgo, true}} {
		t.Run(tt.since, func(t *testing.T) {
			src := providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: tt.since}
			last := map[string]providers.Facts{}
			var rows []providers.Listing
			lists := map[string]string{}
			for _, plan := range []string{"first", "second"} {
				rows = append(rows, credentialed("http://BASE", plan))
				lists[plan] = modelList("glm-5")
				last[plan] = providers.Facts{ReadOn: "2026-10-07", Leaderboard: src, Feed: providers.FeedFacts{Source: src}}
			}
			plans, _, err := unreadRun(t, rows, last, lists, func(r *planrefresh.Refresher) {
				r.BoardErr, r.FeedErr = context.DeadlineExceeded, context.DeadlineExceeded
			})
			if !tt.red {
				if err != nil {
					t.Fatalf("err = %v, want shared sources unread for 21 days green", err)
				}
				return
			}
			if err == nil || strings.Count(err.Error(), planrefresh.SourceLeaderboard) != 1 || strings.Count(err.Error(), planrefresh.SourceFreeFeed) != 1 {
				t.Fatalf("err = %v, want each shared source named once", err)
			}
			if _, ok := plans.facts["second"]; !ok {
				t.Fatal("the second plan was not refreshed before the run failed")
			}
		})
	}
}

// TestRefreshPlansFailsASourceWhoseRecordedDateCannotBeTrusted asserts a source
// recorded unread since a date that is not a date, or that is after the read,
// fails the run rather than reading as freshly unread.
func TestRefreshPlansFailsASourceWhoseRecordedDateCannotBeTrusted(t *testing.T) {
	for _, since := range []string{"not-a-date", "2099-01-01"} {
		t.Run(since, func(t *testing.T) {
			src := providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: since}
			rows, last, lists := unreadPlan(t, unreadKinds[1], &src)
			if _, _, err := unreadRun(t, rows, last, lists, nil); err == nil || !strings.Contains(err.Error(), since) {
				t.Fatalf("err = %v, want the run failed naming %s", err, since)
			}
		})
	}
}

// TestRefreshPlansReportsARefusedModelListOnce asserts a model list that answered
// and was refused fails the run under its own event alone, not also as an unread
// source.
func TestRefreshPlansReportsARefusedModelListOnce(t *testing.T) {
	rows := []providers.Listing{credentialed("http://BASE", "configured")}
	plans, records, err := unreadRun(t, rows, nil, map[string]string{"configured": modelList("New")}, nil)
	if err == nil || strings.Count(err.Error(), "configured") != 1 {
		t.Fatalf("err = %v, want the plan named once for the refusal", err)
	}
	if got := withEvent(records, "plansRefreshSourceUnread"); len(got) != 0 {
		t.Fatalf("unread records %v, want none beside the refusal", got)
	}
	if got := withEvent(records, "planModelListRefused"); len(got) != 1 {
		t.Fatalf("refusal records %v, want one", got)
	}
	if _, written := plans.facts["configured"]; !written {
		t.Fatal("the plan's facts were not written")
	}
}

// TestRefreshPlansNeverAgesAPlanWithNoModelList asserts a plan with no API base to
// read a model list from stays green however long ago it was first found without
// one, logs nothing for it, and records it as not applicable.
func TestRefreshPlansNeverAgesAPlanWithNoModelList(t *testing.T) {
	for _, since := range []string{twentyTwoDaysAgo, "2026-07-06"} {
		t.Run(since, func(t *testing.T) {
			src := providers.Source{State: providers.ReadCouldNotRead, FirstUnreadOn: since}
			rows, last, lists := unreadPlan(t, unreadKinds[1], &src)
			plans, records, err := unreadRun(t, rows, last, lists, func(r *planrefresh.Refresher) {
				r.Vendor.Base = func(string) string { return "" }
			})
			if err != nil {
				t.Fatalf("err = %v, want a plan with no model list green", err)
			}
			if got := withEvent(records, "plansRefreshSourceUnread"); len(got) != 0 {
				t.Fatalf("unread records %v, want none for a source that does not apply", got)
			}
			if got := plans.facts["configured"].Models.Source.State; got != providers.ReadNotApplicable {
				t.Fatalf("model list state = %q, want %q", got, providers.ReadNotApplicable)
			}
		})
	}
}

// TestRefreshPlansFailsAModelListThatAnswers404 asserts a plan with an API base
// whose model list answers 404 is aged like any unread source: a genuine dead
// source, unlike a plan with no API base.
func TestRefreshPlansFailsAModelListThatAnswers404(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gone.Close)
	src := providers.Source{State: providers.ReadCouldNotRead, LastGoodReadOn: twentyTwoDaysAgo}
	rows, last, lists := unreadPlan(t, unreadKinds[1], &src)
	_, _, err := unreadRun(t, rows, last, lists, func(r *planrefresh.Refresher) {
		r.Vendor.Base = func(string) string { return gone.URL }
	})
	if err == nil || !strings.Contains(err.Error(), planrefresh.SourceModelList) {
		t.Fatalf("err = %v, want the model list named as stale", err)
	}
}

// TestRefreshPlansAgesALegacyUnreadSourceFromTheReadThatRecordedIt asserts a
// source a document written before first-unread dates existed records as
// could-not-read under no date at all counts from that document's own read date:
// 21 days later is green and 22 is red.
func TestRefreshPlansAgesALegacyUnreadSourceFromTheReadThatRecordedIt(t *testing.T) {
	for _, kind := range unreadKinds {
		for _, tt := range []struct {
			on  time.Time
			red bool
		}{{time.Date(2026, 12, 17, 12, 0, 0, 0, time.UTC), false}, {time.Date(2026, 12, 18, 12, 0, 0, 0, time.UTC), true}} {
			t.Run(kind.name+" read "+tt.on.Format("2006-01-02"), func(t *testing.T) {
				src := providers.Source{State: providers.ReadCouldNotRead}
				rows, last, lists := unreadPlan(t, kind, &src)
				facts := last["configured"]
				facts.ReadOn = "2026-11-26"
				last["configured"] = facts
				_, _, err := unreadRun(t, rows, last, lists, func(r *planrefresh.Refresher) {
					r.Now = func() time.Time { return tt.on }
				})
				if tt.red != (err != nil) || (tt.red && !strings.Contains(err.Error(), "2026-11-26")) {
					t.Fatalf("err = %v, want red %v naming 2026-11-26", err, tt.red)
				}
			})
		}
	}
}
