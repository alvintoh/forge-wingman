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
	webhookPrefix   = "webhook-"
	sessionIDField  = "session_id"
	receivedAtField = "received_at"
)

// Markers keeps one dispatch/webhook-<session id> document per Linear agent
// session the webhook has taken, so a redelivery acknowledges and wakes nothing.
type Markers struct {
	client *firestore.Client
}

// NewMarkers returns a Markers backed by client.
func NewMarkers(client *firestore.Client) *Markers {
	return &Markers{client: client}
}

// Mark creates the session's marker and, in the same transaction, binds the
// issue to the session as dispatch/session-<issue>, so whoever asks a decision
// in the run can find the session it belongs to. It reports false when the
// marker already exists.
func (m *Markers) Mark(ctx context.Context, sessionID, issue string, at time.Time) (bool, error) {
	marker := m.client.Collection(dispatchCollection).Doc(webhookPrefix + sessionID)
	var created bool
	err := m.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		created = false
		if _, err := tx.Get(marker); err == nil {
			return nil
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		if err := tx.Set(marker, map[string]any{
			sessionIDField:  sessionID,
			ticketIDField:   issue,
			receivedAtField: at,
		}); err != nil {
			return err
		}
		if issue != "" {
			if err := tx.Set(m.client.Collection(dispatchCollection).Doc(sessionBindingPrefix+issue), map[string]any{
				sessionIDField:  sessionID,
				receivedAtField: at,
			}); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("marking session %s: %w", sessionID, err)
	}
	return created, nil
}

// Unmark removes the session's marker; a missing one is not an error.
func (m *Markers) Unmark(ctx context.Context, sessionID string) error {
	if _, err := m.client.Collection(dispatchCollection).Doc(webhookPrefix + sessionID).Delete(ctx); err != nil {
		return fmt.Errorf("unmarking session %s: %w", sessionID, err)
	}
	return nil
}
