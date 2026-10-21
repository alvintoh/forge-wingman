package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const reachCollection = "repo_reach"

// Reaches keeps one repo_reach document per listed repository, rewritten each poll.
type Reaches struct {
	client *firestore.Client
}

// NewReaches returns a Reaches backed by client.
func NewReaches(client *firestore.Client) *Reaches {
	return &Reaches{client: client}
}

// RecordReach writes every record, continuing past a failed write so one bad
// document does not hide the rest.
func (r *Reaches) RecordReach(ctx context.Context, reaches []dispatcher.RepoReach) error {
	var errs []error
	for _, reach := range reaches {
		doc := r.client.Collection(reachCollection).Doc(projectDocID(reach.Repository))
		_, err := doc.Set(ctx, map[string]any{
			"reachable":  reach.Reachable,
			"reason":     string(reach.Reason),
			"status":     reach.Status,
			"checked_at": reach.CheckedAt,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("recording the reach of %s: %w", reach.Repository, err))
		}
	}
	return errors.Join(errs...)
}

// Reads returns each repository's last read, keyed by lower-cased repository.
func (r *Reaches) Reads(ctx context.Context) (map[string]dispatcher.RepoReach, error) {
	docs, err := r.client.Collection(reachCollection).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("reading the repository reach: %w", err)
	}
	out := make(map[string]dispatcher.RepoReach, len(docs))
	for _, doc := range docs {
		var stored struct {
			Reachable bool      `firestore:"reachable"`
			Reason    string    `firestore:"reason"`
			Status    int       `firestore:"status"`
			CheckedAt time.Time `firestore:"checked_at"`
		}
		if err := doc.DataTo(&stored); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", doc.Ref.Path, err)
		}
		repo := strings.ReplaceAll(doc.Ref.ID, "~", "/")
		out[repoKey(repo)] = dispatcher.RepoReach{
			Repository: repo, Reachable: stored.Reachable, Reason: dispatcher.ReachReason(stored.Reason),
			Status: stored.Status, CheckedAt: stored.CheckedAt,
		}
	}
	return out, nil
}
