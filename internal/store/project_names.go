package store

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// projectNamesCollection is the collection the record of Linear project names
// lives in.
const projectNamesCollection = "linear_projects"

// projectNamesDoc is the one document that record lives in; each poll replaces
// it whole, so a project renamed or removed in Linear leaves nothing of its old
// name behind.
const projectNamesDoc = "current"

// ProjectNames keeps the names of the Linear team's projects, rewritten each
// poll, in the default database the surface reads.
type ProjectNames struct {
	client *firestore.Client
}

// NewProjectNames returns a ProjectNames backed by client.
func NewProjectNames(client *firestore.Client) *ProjectNames {
	return &ProjectNames{client: client}
}

// RecordProjectNames replaces the record with the names this poll read.
func (p *ProjectNames) RecordProjectNames(ctx context.Context, names []string) error {
	if names == nil {
		names = []string{}
	}
	if _, err := p.client.Collection(projectNamesCollection).Doc(projectNamesDoc).Set(ctx, map[string]any{"names": names}); err != nil {
		return fmt.Errorf("recording the Linear project names: %w", err)
	}
	return nil
}

// Names returns the names the last poll that could read them recorded, empty
// when no poll has recorded any.
func (p *ProjectNames) Names(ctx context.Context) ([]string, error) {
	snap, err := p.client.Collection(projectNamesCollection).Doc(projectNamesDoc).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the Linear project names: %w", err)
	}
	var stored struct {
		Names []string `firestore:"names"`
	}
	if err := snap.DataTo(&stored); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", snap.Ref.Path, err)
	}
	if stored.Names == nil {
		stored.Names = []string{}
	}
	return stored.Names, nil
}
