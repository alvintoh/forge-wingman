package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	pausedField           = "paused"
	pausedAtField         = "paused_at"
	startPhaseField       = "start_phase"
	startRequestedAtField = "start_requested_at"
	startRefusalField     = "start_refusal"
)

// ErrRepositoryNotListed reports a pause of a repository no listed project names.
var ErrRepositoryNotListed = errors.New("repository not listed")

// phaseDoc is the part of a project document that holds the owner's pause, a
// project with no paused field being running, and the owner's start request.
type phaseDoc struct {
	LinearProject    string    `firestore:"linear_project"`
	Repository       string    `firestore:"repository"`
	AddedAt          time.Time `firestore:"added_at"`
	Paused           bool      `firestore:"paused"`
	StartPhase       string    `firestore:"start_phase"`
	StartRequestedAt time.Time `firestore:"start_requested_at"`
	StartRefusal     string    `firestore:"start_refusal"`
}

// Phases keeps the owner's pause and start request on each listed project, in
// the owner database.
type Phases struct {
	client *firestore.Client
}

// NewPhases returns a Phases backed by client, which must be one opened on
// OwnerDatabase.
func NewPhases(client *firestore.Client) *Phases {
	return &Phases{client: client}
}

// Paused returns the lower-cased repository of every project the owner has paused.
func (p *Phases) Paused(ctx context.Context) (map[string]bool, error) {
	docs, err := p.client.Collection(projectsCollection).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("reading the listed projects: %w", err)
	}
	paused := map[string]bool{}
	for _, doc := range docs {
		var phase phaseDoc
		if err := doc.DataTo(&phase); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", doc.Ref.Path, err)
		}
		if phase.Paused {
			paused[strings.ToLower(phase.Repository)] = true
		}
	}
	return paused, nil
}

// Set records whether the owner has paused repository, and when. It updates the
// two phase fields alone, so the project's other fields stand, and a repository
// no project lists is ErrRepositoryNotListed with nothing written.
func (p *Phases) Set(ctx context.Context, repository string, paused bool, at time.Time) error {
	doc := p.client.Collection(projectsCollection).Doc(projectDocID(repository))
	_, err := doc.Update(ctx, []firestore.Update{
		{Path: pausedField, Value: paused},
		{Path: pausedAtField, Value: at},
	})
	switch {
	case status.Code(err) == codes.NotFound:
		return fmt.Errorf("%w: %s", ErrRepositoryNotListed, repository)
	case err != nil:
		return fmt.Errorf("setting the pause of %s: %w", repository, err)
	}
	return nil
}

// Listed returns every listed project with its pause and start request.
func (p *Phases) Listed(ctx context.Context) ([]dispatcher.PhaseProject, error) {
	docs, err := p.client.Collection(projectsCollection).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("reading the listed projects: %w", err)
	}
	projects := make([]dispatcher.PhaseProject, 0, len(docs))
	for _, doc := range docs {
		var d phaseDoc
		if err := doc.DataTo(&d); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", doc.Ref.Path, err)
		}
		projects = append(projects, dispatcher.PhaseProject{
			Repository: d.Repository, LinearProject: d.LinearProject, AddedAt: d.AddedAt, Paused: d.Paused,
			StartPhase: d.StartPhase, StartRequestedAt: d.StartRequestedAt, StartRefusal: d.StartRefusal,
		})
	}
	return projects, nil
}

// RequestStart records the owner's request to start phase on repository's project,
// clearing the refusal an earlier one drew. A repository no project lists is
// ErrRepositoryNotListed with nothing written.
func (p *Phases) RequestStart(ctx context.Context, repository, phase string, at time.Time) error {
	_, err := p.client.Collection(projectsCollection).Doc(projectDocID(repository)).Update(ctx, []firestore.Update{
		{Path: startPhaseField, Value: phase},
		{Path: startRequestedAtField, Value: at},
		{Path: startRefusalField, Value: firestore.Delete},
	})
	switch {
	case status.Code(err) == codes.NotFound:
		return fmt.Errorf("%w: %s", ErrRepositoryNotListed, repository)
	case err != nil:
		return fmt.Errorf("requesting a start on %s: %w", repository, err)
	}
	return nil
}

// ResolveStart clears the start request made at requestedAt, recording refusal
// against the project when it is not empty. A request made since is left alone.
func (p *Phases) ResolveStart(ctx context.Context, repository string, requestedAt time.Time, refusal string) error {
	doc := p.client.Collection(projectsCollection).Doc(projectDocID(repository))
	return p.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(doc)
		if err != nil {
			return fmt.Errorf("reading %s: %w", repository, err)
		}
		var d phaseDoc
		if err := snap.DataTo(&d); err != nil {
			return fmt.Errorf("decoding %s: %w", doc.Path, err)
		}
		if !d.StartRequestedAt.Equal(requestedAt) {
			return nil
		}
		updates := []firestore.Update{
			{Path: startPhaseField, Value: firestore.Delete},
			{Path: startRequestedAtField, Value: firestore.Delete},
			{Path: startRefusalField, Value: firestore.Delete},
		}
		if refusal != "" {
			updates[2].Value = refusal
		}
		return tx.Update(doc, updates)
	})
}
