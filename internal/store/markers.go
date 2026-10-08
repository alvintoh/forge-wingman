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

// Mark creates the session's marker, reporting false when one already exists.
// issue is recorded beside it for whoever reads the store.
func (m *Markers) Mark(ctx context.Context, sessionID, issue string, at time.Time) (bool, error) {
	_, err := m.client.Collection(dispatchCollection).Doc(webhookPrefix+sessionID).Create(ctx, map[string]any{
		sessionIDField:  sessionID,
		ticketIDField:   issue,
		receivedAtField: at,
	})
	if status.Code(err) == codes.AlreadyExists {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("marking session %s: %w", sessionID, err)
	}
	return true, nil
}

// Unmark removes the session's marker; a missing one is not an error.
func (m *Markers) Unmark(ctx context.Context, sessionID string) error {
	if _, err := m.client.Collection(dispatchCollection).Doc(webhookPrefix + sessionID).Delete(ctx); err != nil {
		return fmt.Errorf("unmarking session %s: %w", sessionID, err)
	}
	return nil
}
