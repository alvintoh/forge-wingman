package store

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	prov "github.com/alvintoh/forge-wingman/internal/providers"
)

const (
	// plansCollection holds one provider_plans/<provider> document per plan,
	// written only by the owner's runner commands.
	plansCollection = "provider_plans"
	// factsCollection is the subcollection of a plan the weekly refresh writes,
	// one document per read date.
	factsCollection = "facts"
)

// Plans reads and writes the owner's provider plan records.
type Plans struct {
	client *firestore.Client
}

// NewPlans returns a Plans backed by client.
func NewPlans(client *firestore.Client) *Plans { return &Plans{client: client} }

func (p *Plans) doc(provider string) *firestore.DocumentRef {
	return p.client.Collection(plansCollection).Doc(provider)
}

// PutDefinition writes provider's definition, leaving any verdict and replies
// already recorded as they are.
func (p *Plans) PutDefinition(ctx context.Context, provider string, def prov.Definition) error {
	if _, err := p.doc(provider).Set(ctx, map[string]any{"definition": def}, firestore.Merge(firestore.FieldPath{"definition"})); err != nil {
		return fmt.Errorf("writing plan %s: %w", provider, err)
	}
	return nil
}

// RecordVerdict records the verdict and its evidence on provider's plan, which
// must already be defined.
func (p *Plans) RecordVerdict(ctx context.Context, provider string, note prov.VerdictNote) error {
	_, err := p.doc(provider).Update(ctx, []firestore.Update{
		{Path: "verdict", Value: note.Verdict},
		{Path: "verdict_wording", Value: note.Wording},
		{Path: "verdict_source", Value: note.Source},
		{Path: "verdict_read_on", Value: note.ReadOn},
	})
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("plan %s is not defined", provider)
	}
	if err != nil {
		return fmt.Errorf("recording the verdict on plan %s: %w", provider, err)
	}
	return nil
}

// AddReply appends a vendor reply to provider's plan and sets the verdict in
// the same write, so a reply never lands without the verdict it was read for.
func (p *Plans) AddReply(ctx context.Context, provider string, reply prov.Reply, verdict prov.Verdict) error {
	_, err := p.doc(provider).Update(ctx, []firestore.Update{
		{Path: "replies", Value: firestore.ArrayUnion(reply)},
		{Path: "verdict", Value: verdict},
	})
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("plan %s is not defined", provider)
	}
	if err != nil {
		return fmt.Errorf("recording a reply on plan %s: %w", provider, err)
	}
	return nil
}

// Verdict reports the verdict recorded for provider. A provider with no plan,
// or a plan with no verdict yet, is unconfirmed.
func (p *Plans) Verdict(ctx context.Context, provider string) (prov.Verdict, error) {
	snap, err := p.doc(provider).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return prov.VerdictUnconfirmed, nil
	}
	if err != nil {
		return "", fmt.Errorf("reading plan %s: %w", provider, err)
	}
	var plan prov.Plan
	if err := snap.DataTo(&plan); err != nil {
		return "", fmt.Errorf("decoding plan %s: %w", provider, err)
	}
	if plan.Verdict == "" {
		return prov.VerdictUnconfirmed, nil
	}
	return plan.Verdict, nil
}

// List reads every plan with the date of its latest refreshed facts, ordered by
// provider.
func (p *Plans) List(ctx context.Context) ([]prov.Listing, error) {
	snaps, err := p.client.Collection(plansCollection).OrderBy(firestore.DocumentID, firestore.Asc).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("listing plans: %w", err)
	}
	rows := make([]prov.Listing, 0, len(snaps))
	for _, snap := range snaps {
		var plan prov.Plan
		if err := snap.DataTo(&plan); err != nil {
			return nil, fmt.Errorf("decoding plan %s: %w", snap.Ref.ID, err)
		}
		date, err := p.latestFactsDate(ctx, snap.Ref)
		if err != nil {
			return nil, err
		}
		rows = append(rows, prov.Listing{Provider: snap.Ref.ID, Plan: plan, FactsDate: date})
	}
	return rows, nil
}

// latestFactsDate is the id, a read date, of the newest document in plan's
// facts subcollection, or "" when there is none. It reads the ids ascending and
// keeps the last, since a descending key scan is unsupported.
func (p *Plans) latestFactsDate(ctx context.Context, plan *firestore.DocumentRef) (string, error) {
	snaps, err := plan.Collection(factsCollection).Select().OrderBy(firestore.DocumentID, firestore.Asc).Documents(ctx).GetAll()
	if err != nil {
		return "", fmt.Errorf("reading the facts of plan %s: %w", plan.ID, err)
	}
	if len(snaps) == 0 {
		return "", nil
	}
	return snaps[len(snaps)-1].Ref.ID, nil
}
