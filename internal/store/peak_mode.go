package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	peakModeDocID = "peak-mode"
	modeField     = "mode"
	setAtField    = "set_at"
)

// PeakModes keeps the dispatch/peak-mode document the owner sets and every poll reads.
type PeakModes struct {
	client *firestore.Client
}

// NewPeakModes returns a PeakModes backed by client.
func NewPeakModes(client *firestore.Client) *PeakModes {
	return &PeakModes{client: client}
}

func (p *PeakModes) doc() *firestore.DocumentRef {
	return p.client.Collection(dispatchCollection).Doc(peakModeDocID)
}

// Mode returns the stored mode, off-peak-only while none is set. A stored value
// naming no mode is an error, so a caller does not guess what it meant.
func (p *PeakModes) Mode(ctx context.Context) (dispatcher.PeakMode, error) {
	snap, err := p.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.PeakOffPeakOnly, nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the peak mode: %w", err)
	}
	stored, _ := snap.Data()[modeField].(string)
	mode := dispatcher.PeakMode(stored)
	if !mode.Valid() {
		return "", fmt.Errorf("reading the peak mode: %w %q", dispatcher.ErrUnknownPeakMode, stored)
	}
	return mode, nil
}

// Set stores mode, effective from the next poll: an operator's runner peak-mode
// action.
func (p *PeakModes) Set(ctx context.Context, mode dispatcher.PeakMode, at time.Time) error {
	if !mode.Valid() {
		return fmt.Errorf("setting the peak mode: %w %q", dispatcher.ErrUnknownPeakMode, string(mode))
	}
	if _, err := p.doc().Set(ctx, map[string]any{modeField: string(mode), setAtField: at}); err != nil {
		return fmt.Errorf("setting the peak mode: %w", err)
	}
	return nil
}
