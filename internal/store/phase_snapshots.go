package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	phaseSnapshotCollection = "project_phases"
	linearProjectField      = "linear_project"
	snapshotAtField         = "at"
)

// PhaseSnapshots keeps one project_phases document per listed project, rewritten
// each poll, in the default database.
type PhaseSnapshots struct {
	client *firestore.Client
	logger *slog.Logger
}

// NewPhaseSnapshots returns a PhaseSnapshots backed by client, logging to the
// default logger.
func NewPhaseSnapshots(client *firestore.Client) *PhaseSnapshots {
	return &PhaseSnapshots{client: client, logger: slog.Default()}
}

// WithLogger returns a PhaseSnapshots over the same client that logs to logger.
func (s *PhaseSnapshots) WithLogger(logger *slog.Logger) *PhaseSnapshots {
	return &PhaseSnapshots{client: s.client, logger: logger}
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

// maxSnapshotAge is how far a snapshot's read time may sit from now for its
// project to count as driven. The dispatcher rewrites every listed project's
// snapshot each poll, every 15 minutes, and deletes none when a project is
// de-listed, so one not rewritten in four polls belongs to a project no longer
// listed. A time as far ahead of the clock is not trusted either.
const maxSnapshotAge = time.Hour

// DrivesLinearProject reports whether a snapshot read within maxSnapshotAge of
// at names linearProject, compared case-insensitively as the poll matches a
// ticket's project: the projects the dispatcher drives, read from the default
// database, since the owner's own list lives where the webhook may not read
// (adr/0019). A document that does not decode is logged and skipped, so one bad
// snapshot does not fail every answer.
func (s *PhaseSnapshots) DrivesLinearProject(ctx context.Context, linearProject string, at time.Time) (bool, error) {
	if linearProject == "" {
		return false, nil
	}
	docs, err := s.client.Collection(phaseSnapshotCollection).Select(linearProjectField, snapshotAtField).Documents(ctx).GetAll()
	if err != nil {
		return false, fmt.Errorf("reading the phase snapshots: %w", err)
	}
	for _, doc := range docs {
		var snap struct {
			LinearProject string    `firestore:"linear_project"`
			At            time.Time `firestore:"at"`
		}
		if err := doc.DataTo(&snap); err != nil {
			s.logger.Warn("phaseSnapshotUndecodable", "doc", doc.Ref.ID, "err", err.Error())
			continue
		}
		if at.Sub(snap.At).Abs() < maxSnapshotAge && strings.EqualFold(snap.LinearProject, linearProject) {
			return true, nil
		}
	}
	return false, nil
}
