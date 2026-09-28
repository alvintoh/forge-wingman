package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// providersCollection holds one providers/<name> document per provider
	// (AC4), mirroring dispatchCollection's rejected-<ticket> pattern.
	providersCollection = "providers"
	// haltThreshold is AC4's "3 consecutive infra stops" — the count at which
	// a provider's document gains a halted_at, withholding dispatch to it.
	haltThreshold = 3
)

// ProviderHealth is one provider's providers/<name> document: how many infra
// stops it has accumulated since its last success or clear, and when that
// count reached haltThreshold (zero while still admitted).
type ProviderHealth struct {
	ConsecutiveStops int       `firestore:"consecutive_stops"`
	HaltedAt         time.Time `firestore:"halted_at"`
}

// Providers reads and writes provider halt state against the providers/<name>
// collection (AC4, AC5).
type Providers struct {
	client *firestore.Client
}

// NewProviders returns a Providers backed by client.
func NewProviders(client *firestore.Client) *Providers { return &Providers{client: client} }

func (p *Providers) doc(provider string) *firestore.DocumentRef {
	return p.client.Collection(providersCollection).Doc(provider)
}

// RecordInfraStop increments provider's stop count, halting dispatch to it
// once the count reaches haltThreshold (AC4) — a halt already set is left as
// it is, naming when it first triggered rather than the most recent stop.
func (p *Providers) RecordInfraStop(ctx context.Context, provider string, at time.Time) error {
	ref := p.doc(provider)
	err := p.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		health, err := readProviderHealth(tx.Get, ref)
		if err != nil {
			return err
		}
		health.ConsecutiveStops++
		if health.ConsecutiveStops >= haltThreshold && health.HaltedAt.IsZero() {
			health.HaltedAt = at
		}
		return tx.Set(ref, health)
	})
	if err != nil {
		return fmt.Errorf("recording an infra stop for provider %s: %w", provider, err)
	}
	return nil
}

// Halted reports whether provider is currently halted from repeated infra
// stops (AC4), until ClearHalt admits it again (AC5).
func (p *Providers) Halted(ctx context.Context, provider string) (bool, error) {
	health, err := readProviderHealth(func(ref *firestore.DocumentRef) (*firestore.DocumentSnapshot, error) {
		return ref.Get(ctx)
	}, p.doc(provider))
	if err != nil {
		return false, fmt.Errorf("reading provider %s: %w", provider, err)
	}
	return !health.HaltedAt.IsZero(), nil
}

// ClearHalt admits provider again, resetting its stop count: an operator's
// runner enable-provider action (AC5), never automatic.
func (p *Providers) ClearHalt(ctx context.Context, provider string) error {
	return p.reset(ctx, provider)
}

// RecordSuccess resets provider's stop count on a run that actually
// succeeded against it — so haltThreshold counts stops CONSECUTIVE since the
// last success, not merely accumulated over the provider's whole history.
func (p *Providers) RecordSuccess(ctx context.Context, provider string) error {
	return p.reset(ctx, provider)
}

func (p *Providers) reset(ctx context.Context, provider string) error {
	if _, err := p.doc(provider).Set(ctx, ProviderHealth{}); err != nil {
		return fmt.Errorf("resetting provider %s: %w", provider, err)
	}
	return nil
}

// readProviderHealth reads provider's document through get, reporting a zero
// ProviderHealth for one that does not exist yet — a provider with no
// recorded stop is exactly as healthy as one explicitly cleared.
func readProviderHealth(get func(*firestore.DocumentRef) (*firestore.DocumentSnapshot, error), ref *firestore.DocumentRef) (ProviderHealth, error) {
	snap, err := get(ref)
	if status.Code(err) == codes.NotFound {
		return ProviderHealth{}, nil
	}
	if err != nil {
		return ProviderHealth{}, err
	}
	var health ProviderHealth
	if err := snap.DataTo(&health); err != nil {
		return ProviderHealth{}, fmt.Errorf("decoding provider %s: %w", ref.ID, err)
	}
	return health, nil
}
