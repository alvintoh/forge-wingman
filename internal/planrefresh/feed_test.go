package planrefresh

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// feedReply is the free feed's own reply: one paid plan and one free one.
const feedReply = `{"plans":[
	{"provider":"command-code","price_usd":12,"limit_behaviour":"can-spend-past","models":["glm-5"],"perks":["No training on your data"]},
	{"provider":"gemini","price_usd":0,"limit_behaviour":"hard-stop","models":["gemini-3.8-flash"],"perks":[]}]}`

func TestFeedReadsWhatItStatesAboutEachProvider(t *testing.T) {
	srv := bodyServer(t, http.StatusOK, feedReply)
	got, err := (Feed{Endpoint: srv.URL, HTTP: srv.Client()}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, listed := got.Plans["command-code"]
	if !listed || plan.Price != 12*money.Dollar || plan.LimitBehaviour != providers.LimitCanSpendPast ||
		!slices.Equal(plan.Models, []string{"glm-5"}) || !slices.Equal(plan.Perks, []string{"No training on your data"}) {
		t.Fatalf("command-code is %+v (listed %t), want the feed's own figures for it", plan, listed)
	}
	if free := got.Plans["gemini"]; free.Price != 0 {
		t.Fatalf("gemini price %v, want the feed's own zero", free.Price)
	}
	if _, listed := got.Plans["absent"]; listed {
		t.Fatal("the feed reports a provider it does not list")
	}
}

// TestFeedRefusesWhatItCannotRead asserts an error status and a reply this build
// cannot read are refused, so a refresh records the feed as unread rather than as
// a feed stating nothing about the plan.
func TestFeedRefusesWhatItCannotRead(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"a 500", http.StatusInternalServerError, "boom"},
		{"a malformed body", http.StatusOK, `{"plans":"not a list"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := bodyServer(t, tt.status, tt.body)
			if _, err := (Feed{Endpoint: srv.URL, HTTP: srv.Client()}).Read(context.Background()); err == nil {
				t.Fatal("Read accepted what it cannot read")
			}
		})
	}
}
