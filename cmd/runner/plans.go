package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
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
	provider := fs.String("provider", "", "provider the plan belongs to")
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

// planOptIn records one of the owner's consents on a plan, or withdraws it: to
// spend on a per-token provider, with -private to private repositories' runs on
// its free tier, or with -harness to a harness that is not open-source, which no
// run selects without it. No run job writes any of them, so a ticket or a run
// cannot opt a provider or a harness in.
func planOptIn(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plan-optin", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider whose per-token spend the owner accepts; per provider, and cleared if its billing is later changed")
	optedIn := fs.Bool("opted-in", true, "false withdraws the opt-in")
	private := fs.Bool("private", false, "record the opt-in to private repositories' runs on the provider's free tier instead")
	harness := fs.String("harness", "", "record the opt-in to this harness on the provider's plan instead, for a harness that is not open-source")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *private && *harness != "" {
		return errors.New("-private and -harness are separate opt-ins")
	}
	plans, closeStore, err := openPlans(ctx, e, *provider, true, nil)
	if err != nil {
		return err
	}
	defer closeStore()
	if *harness != "" {
		if err := plans.SetHarnessOptIn(ctx, *provider, *harness, *optedIn); err != nil {
			return err
		}
		logger.Info("planHarnessOptInRecorded", "provider", *provider, "harness", *harness, "optedIn", *optedIn)
		return nil
	}
	set := plans.SetOptIn
	if *private {
		set = plans.SetPrivateOptIn
	}
	if err := set(ctx, *provider, *optedIn); err != nil {
		return err
	}
	logger.Info("planOptInRecorded", "provider", *provider, "optedIn", *optedIn, "private", *private)
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

// planList prints every plan side by side, flagging the ones to look at, with
// each plan's harness pairs and their licences.
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
	licences := harnessLicences(e.harnesses)
	for i := range rows {
		rows[i].Harnesses = harnessLines(rows[i].Plan.Definition.Harnesses, licences)
	}
	return providers.WriteTable(os.Stdout, rows)
}

// harnessLines renders a plan's pairs as the side-by-side view lists them:
// open-source pairs first, keeping the plan's own order within each group.
func harnessLines(pairs []providers.HarnessPair, licences map[string]runner.Licence) []providers.HarnessLine {
	lines := make([]providers.HarnessLine, 0, len(pairs))
	for _, p := range pairs {
		lines = append(lines, providers.HarnessLine{Harness: p.Harness, Model: p.Model, Licence: string(licences[p.Harness])})
	}
	slices.SortStableFunc(lines, func(a, b providers.HarnessLine) int {
		return licenceRank(a.Licence) - licenceRank(b.Licence)
	})
	return lines
}

// licenceRank puts the open-source pairs of a plan's row first, since they are
// the ones every run may select.
func licenceRank(licence string) int {
	if licence == string(runner.LicenceOpenSource) {
		return 0
	}
	return 1
}

// harnessLicences names the licence each harness this build registers runs
// under, as the adapter that owns it records.
func harnessLicences(harnesses []runner.Harness) map[string]runner.Licence {
	licences := make(map[string]runner.Licence, len(harnesses))
	for _, h := range harnesses {
		licences[h.Name()] = h.Licence()
	}
	return licences
}

// harnessOptIn reads the opt-in each of models' plans records and returns the
// resolver a command that runs agents consults before it may select a harness,
// from the plan store the owner CLI alone writes (plan-optin -harness). It is the
// only way a harness that is not open-source is admitted: a command with no
// project to read the store from — every run job — consults no opt-in at all,
// and a plan whose record cannot be read contributes none.
func harnessOptIn(ctx context.Context, logger *slog.Logger, getenv func(string) string, models []string) runner.HarnessOptIn {
	project := getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		return nil
	}
	fsc, err := firestore.NewClient(ctx, project)
	if err != nil {
		logger.Warn("harnessOptInUnread", "err", err.Error())
		return nil
	}
	defer func() { _ = fsc.Close() }()
	plans := store.NewPlans(fsc)
	optedIn := make(map[string]map[string]bool, len(models))
	for _, model := range models {
		plan := runner.Provider(model)
		if plan == "" {
			continue
		}
		if _, read := optedIn[plan]; read {
			continue
		}
		record, ok, err := plans.Plan(ctx, plan)
		if err != nil {
			logger.Warn("harnessOptInUnread", "plan", plan, "err", err.Error())
			continue
		}
		names := map[string]bool{}
		if ok {
			for _, harness := range record.HarnessOptIn {
				names[harness] = true
			}
		}
		optedIn[plan] = names
	}
	return func(plan, harness string) bool { return optedIn[plan][harness] }
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
