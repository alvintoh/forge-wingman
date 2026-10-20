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
)

const (
	pausedField   = "paused"
	pausedAtField = "paused_at"
)

// ErrRepositoryNotListed reports a pause of a repository no listed project names.
var ErrRepositoryNotListed = errors.New("repository not listed")

// phaseDoc is the part of a project document that holds the owner's pause; a
// project with no paused field is running.
type phaseDoc struct {
	Repository string `firestore:"repository"`
	Paused     bool   `firestore:"paused"`
}

// Phases keeps the owner's pause on each listed project, in the owner database.
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
