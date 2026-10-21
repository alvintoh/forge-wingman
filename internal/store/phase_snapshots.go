package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const phaseSnapshotCollection = "project_phases"

// PhaseSnapshots keeps one project_phases document per listed project, rewritten
// each poll, in the default database.
type PhaseSnapshots struct {
	client *firestore.Client
}

// NewPhaseSnapshots returns a PhaseSnapshots backed by client.
func NewPhaseSnapshots(client *firestore.Client) *PhaseSnapshots {
	return &PhaseSnapshots{client: client}
}

// RecordSnapshots writes every snapshot, continuing past a failed write so one
// bad document does not hide the rest.
func (s *PhaseSnapshots) RecordSnapshots(ctx context.Context, snapshots []dispatcher.PhaseSnapshot) error {
	var errs []error
	for _, snap := range snapshots {
		if _, err := s.client.Collection(phaseSnapshotCollection).Doc(projectDocID(snap.Repository)).Set(ctx, snap); err != nil {
			errs = append(errs, fmt.Errorf("recording the phases of %s: %w", snap.Repository, err))
		}
	}
	return errors.Join(errs...)
}

// repoKey is how the readers key a repository: lower-cased, as GitHub names one
// case-insensitively.
func repoKey(repository string) string { return strings.ToLower(repository) }

// Snapshots returns every snapshot, keyed by lower-cased repository.
func (s *PhaseSnapshots) Snapshots(ctx context.Context) (map[string]dispatcher.PhaseSnapshot, error) {
	docs, err := s.client.Collection(phaseSnapshotCollection).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("reading the phase snapshots: %w", err)
	}
	out := make(map[string]dispatcher.PhaseSnapshot, len(docs))
	for _, doc := range docs {
		var snap dispatcher.PhaseSnapshot
		if err := doc.DataTo(&snap); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", doc.Ref.Path, err)
		}
		out[repoKey(snap.Repository)] = snap
	}
	return out, nil
}
