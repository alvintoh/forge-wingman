package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	breakerDocID     = "breaker"
	noticePrefix     = "notice-"
	noticeClaimLimit = 20

	classField       = "class"
	linkField        = "link"
	noticeStateField = "notice_state"
	raisedAtField    = "raised_at"
	postedAtField    = "posted_at"
	trippedAtField   = "tripped_at"
	runIDField       = "run_id"
)

// The states a notice holds.
const (
	noticePending = "pending"
	noticeClaimed = "claimed"
	noticePosted  = "posted"
)

// Breaker keeps the dispatch/breaker document, whose presence holds every
// dispatch after a systemic stop.
type Breaker struct {
	client *firestore.Client
}

// NewBreaker returns a Breaker backed by client.
func NewBreaker(client *firestore.Client) *Breaker {
	return &Breaker{client: client}
}

func (b *Breaker) doc() *firestore.DocumentRef {
	return b.client.Collection(dispatchCollection).Doc(breakerDocID)
}

// Trip trips the breaker for run runID's stop of class. A breaker already
// tripped keeps the stop that first tripped it.
func (b *Breaker) Trip(ctx context.Context, runID, class string, at time.Time) error {
	_, err := b.doc().Create(ctx, map[string]any{
		runIDField:     runID,
		classField:     class,
		trippedAtField: at,
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("tripping the breaker for run %s: %w", runID, err)
	}
	return nil
}

// Tripped reports whether the breaker is tripped.
func (b *Breaker) Tripped(ctx context.Context) (bool, error) {
	_, err := b.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the breaker: %w", err)
	}
	return true, nil
}

// Reset clears the breaker, admitting dispatch again: an operator's runner
// reset-breaker action, never automatic.
func (b *Breaker) Reset(ctx context.Context) error {
	if _, err := b.doc().Delete(ctx); err != nil {
		return fmt.Errorf("resetting the breaker: %w", err)
	}
	return nil
}

// Notices keeps the systemic-failure notices waiting to be posted. Delivery is
// at least once: a notice whose Posted write fails is posted again once its
// claim goes stale.
type Notices struct {
	client *firestore.Client
}

// NewNotices returns a Notices backed by client.
func NewNotices(client *firestore.Client) *Notices {
	return &Notices{client: client}
}

func (n *Notices) doc(id string) *firestore.DocumentRef {
	return n.client.Collection(dispatchCollection).Doc(noticePrefix + id)
}

// Raise creates the notice, pending, unless one with its ID already exists.
func (n *Notices) Raise(ctx context.Context, notice dispatcher.Notice, at time.Time) error {
	_, err := n.doc(notice.ID).Create(ctx, map[string]any{
		classField:       notice.Class,
		linkField:        notice.Link,
		noticeStateField: noticePending,
		raisedAtField:    at,
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("raising notice %s: %w", notice.ID, err)
	}
	return nil
}

// Claim takes every pending notice, and any claimed before staleBefore, by
// marking it claimed at at in one transaction. Pending and stale notices are
// read apart, so claims still fresh never crowd a pending one out.
func (n *Notices) Claim(ctx context.Context, at, staleBefore time.Time) ([]dispatcher.Notice, error) {
	var claimed []dispatcher.Notice
	notices := n.client.Collection(dispatchCollection)
	err := n.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		claimed = nil
		pending, err := allDocs(tx.Documents(notices.Where(noticeStateField, "==", noticePending).Limit(noticeClaimLimit)))
		if err != nil {
			return err
		}
		stale, err := allDocs(tx.Documents(notices.Where(claimedAtField, "<", staleBefore).Limit(noticeClaimLimit)))
		if err != nil {
			return err
		}
		for _, snap := range append(pending, stale...) {
			data := snap.Data()
			if err := tx.Update(snap.Ref, []firestore.Update{
				{Path: noticeStateField, Value: noticeClaimed},
				{Path: claimedAtField, Value: at},
			}); err != nil {
				return err
			}
			class, _ := data[classField].(string)
			link, _ := data[linkField].(string)
			claimed = append(claimed, dispatcher.Notice{ID: strings.TrimPrefix(snap.Ref.ID, noticePrefix), Class: class, Link: link})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claiming notices: %w", err)
	}
	return claimed, nil
}

// allDocs reads every document iter yields.
func allDocs(iter *firestore.DocumentIterator) ([]*firestore.DocumentSnapshot, error) {
	defer iter.Stop()
	var snaps []*firestore.DocumentSnapshot
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return snaps, nil
		}
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
}

// Posted marks a claimed notice posted, so no poll takes it again.
func (n *Notices) Posted(ctx context.Context, id string, at time.Time) error {
	if _, err := n.doc(id).Update(ctx, []firestore.Update{
		{Path: noticeStateField, Value: noticePosted},
		{Path: postedAtField, Value: at},
		{Path: claimedAtField, Value: firestore.Delete},
	}); err != nil {
		return fmt.Errorf("marking notice %s posted: %w", id, err)
	}
	return nil
}

// Unclaim returns a claimed notice to pending for the next poll.
func (n *Notices) Unclaim(ctx context.Context, id string) error {
	if _, err := n.doc(id).Update(ctx, []firestore.Update{
		{Path: noticeStateField, Value: noticePending},
		{Path: claimedAtField, Value: firestore.Delete},
	}); err != nil {
		return fmt.Errorf("unclaiming notice %s: %w", id, err)
	}
	return nil
}
