package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// listFlag collects a repeatable flag's values.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// planDefine records a provider plan's definition, keeping any verdict already
// recorded against it.
func planDefine(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plan-define", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider the plan belongs to, e.g. opencode")
	name := fs.String("name", "", "plan name")
	price := fs.String("price-usd", "", "monthly price in USD")
	limit := fs.String("limit", string(providers.LimitUnknown), "behaviour at the limit: hard-stop, can-spend-past or unknown")
	billing := fs.String("billing", "", "how the plan charges: free, allowance or per-token")
	var pages, harnesses listFlag
	fs.Var(&pages, "page", "vendor page to read: an https URL, then optionally a selector (repeatable)")
	fs.Var(&harnesses, "harness", "harness:model the plan is offered through (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	def, defErr := providers.NewDefinition(*name, *price, pages, harnesses, *limit, *billing)
	plans, closeStore, err := openPlans(ctx, e, *provider, true, defErr)
	if err != nil {
		return err
	}
	defer closeStore()
	if err := plans.PutDefinition(ctx, *provider, def); err != nil {
		return err
	}
	logger.Info("planDefined", "provider", *provider, "monthlyPriceUSD", def.MonthlyPrice.USD(), "limit", string(def.LimitBehaviour), "billing", string(def.Billing))
	return nil
}

// planOptIn records the owner's consent to spend on a per-token provider, or
// withdraws it. The dispatcher never writes it, so a ticket cannot opt a
// provider in.
func planOptIn(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plan-optin", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider whose per-token spend the owner accepts; per provider, and cleared if its billing is later changed")
	optedIn := fs.Bool("opted-in", true, "false withdraws the opt-in")
	if err := fs.Parse(args); err != nil {
		return err
	}
	plans, closeStore, err := openPlans(ctx, e, *provider, true, nil)
	if err != nil {
		return err
	}
	defer closeStore()
	if err := plans.SetOptIn(ctx, *provider, *optedIn); err != nil {
		return err
	}
	logger.Info("planOptInRecorded", "provider", *provider, "optedIn", *optedIn)
	return nil
}

// planVerdict records the verdict on a plan's terms with the wording, page and
// date it rests on.
func planVerdict(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plan-verdict", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider the verdict is about")
	verdict := fs.String("verdict", "", "allowed, restricted or unconfirmed")
	wording := fs.String("wording", "", "the vendor's own words the verdict rests on")
	source := fs.String("source", "", "https URL of the page the wording came from")
	readOn := fs.String("read-on", "", "date the wording was read, yyyy-mm-dd")
	if err := fs.Parse(args); err != nil {
		return err
	}
	note, noteErr := providers.NewVerdictNote(*verdict, *wording, *source, *readOn)
	plans, closeStore, err := openPlans(ctx, e, *provider, true, noteErr)
	if err != nil {
		return err
	}
	defer closeStore()
	if err := plans.RecordVerdict(ctx, *provider, note); err != nil {
		return err
	}
	logger.Info("planVerdictRecorded", "provider", *provider, "verdict", string(note.Verdict))
	return nil
}

// planReply records a vendor support reply and the verdict it leads to, which
// is required so a reply never sits beside a verdict it did not review.
func planReply(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plan-reply", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider the reply is about")
	text := fs.String("reply", "", "the vendor's reply")
	verdict := fs.String("verdict", "", "the verdict after the reply: allowed, restricted or unconfirmed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now().UTC()
	reply, replyErr := providers.NewReply(*text, now)
	v, verdictErr := providers.ParseVerdict(*verdict)
	plans, closeStore, err := openPlans(ctx, e, *provider, true, errors.Join(replyErr, verdictErr))
	if err != nil {
		return err
	}
	defer closeStore()
	if err := plans.AddReply(ctx, *provider, reply, v); err != nil {
		return err
	}
	logger.Info("planReplyRecorded", "provider", *provider, "verdict", string(v))
	return nil
}

// planList prints every plan side by side, flagging the ones to look at.
func planList(ctx context.Context, _ *slog.Logger, e env, args []string) error {
	if err := flag.NewFlagSet("plan-list", flag.ContinueOnError).Parse(args); err != nil {
		return err
	}
	plans, closeStore, err := openPlans(ctx, e, "", false, nil)
	if err != nil {
		return err
	}
	defer closeStore()
	rows, err := plans.List(ctx)
	if err != nil {
		return err
	}
	return providers.WriteTable(os.Stdout, rows)
}

// openPlans opens the plan store for an owner command: the account must be the
// owner's, and a command naming a provider must name a valid one with valid
// input before Firestore is touched, so a refusal never reaches it.
func openPlans(ctx context.Context, e env, provider string, named bool, inputErr error) (*store.Plans, func(), error) {
	if err := e.identity.CheckAccount(); err != nil {
		return nil, nil, err
	}
	if named && !providerPattern.MatchString(provider) {
		return nil, nil, fmt.Errorf("provider %q is not a provider name", provider)
	}
	if inputErr != nil {
		return nil, nil, inputErr
	}
	if e.project == "" {
		return nil, nil, errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return nil, nil, fmt.Errorf("firestore client: %w", err)
	}
	return store.NewPlans(fsc), func() { _ = fsc.Close() }, nil
}
