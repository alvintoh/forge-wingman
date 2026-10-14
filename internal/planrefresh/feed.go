package planrefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// feedEndpoint is the tokenplans free feed a refresh reads. UNVERIFIED: no probe
// has confirmed the address or the reply's shape, so the command lets the owner
// name another one, and a reply this build cannot read is recorded as the feed
// being unread rather than as a feed that says nothing.
const feedEndpoint = "https://tokenplans.com/api/free-feed"

// Feed reads the tokenplans free feed of plans, which a refresh treats as a
// change signal only.
type Feed struct {
	// Endpoint is the feed's address; empty means the built-in one.
	Endpoint string
	// HTTP is the client requests go through; nil means NewClient().
	HTTP *http.Client
}

// FeedReport is the free feed as read: what it states about each provider it
// lists, keyed by provider name. A provider it does not list is one it says
// nothing about.
type FeedReport struct {
	Plans map[string]FeedPlan
}

// FeedPlan is what the free feed states about one provider. Every figure is the
// feed's own; a refresh records the recorded plan's instead.
type FeedPlan struct {
	Price          money.Micros
	LimitBehaviour providers.LimitBehaviour
	Models         []string
	Perks          []string
}

// Read reads the free feed.
func (f Feed) Read(ctx context.Context) (FeedReport, error) {
	endpoint := f.Endpoint
	if endpoint == "" {
		endpoint = feedEndpoint
	}
	body, err := readBody(ctx, f.HTTP, endpoint, maxReplyBytes)
	if err != nil {
		return FeedReport{}, err
	}
	var reply struct {
		Plans []struct {
			Provider       string   `json:"provider"`
			PriceUSD       float64  `json:"price_usd"`
			LimitBehaviour string   `json:"limit_behaviour"`
			Models         []string `json:"models"`
			Perks          []string `json:"perks"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return FeedReport{}, fmt.Errorf("decoding the free feed: %w", err)
	}
	report := FeedReport{Plans: make(map[string]FeedPlan, len(reply.Plans))}
	for _, p := range reply.Plans {
		provider := providers.CapText(strings.TrimSpace(p.Provider), maxFieldBytes)
		if provider == "" {
			continue
		}
		report.Plans[provider] = FeedPlan{
			Price:          money.FromUSD(p.PriceUSD),
			LimitBehaviour: providers.LimitBehaviour(providers.CapText(strings.TrimSpace(p.LimitBehaviour), maxFieldBytes)),
			Models:         capList(p.Models, maxModels),
			Perks:          capList(p.Perks, maxPerks),
		}
	}
	return report, nil
}
