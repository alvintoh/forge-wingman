package store

import (
	"context"
	"errors"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

func TestTwoSessionsAndTwoPollsQueueOneRun(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("ABC-22")
	first, second := fresh("session"), fresh("session")
	forget(t, client, id)
	t.Cleanup(func() {
		for _, s := range []string{first, second} {
			_, _ = client.Collection(dispatchCollection).Doc(webhookPrefix + s).Delete(context.Background())
		}
	})
	markers := NewMarkers(client)

	for _, s := range []string{first, second} {
		if created, err := markers.Mark(ctx, s, id, queueAt); err != nil || !created {
			t.Fatalf("mark %s = %v, %v", s, created, err)
		}
	}
	if created, err := markers.Mark(ctx, first, id, queueAt); err != nil || created {
		t.Fatalf("redelivered mark = %v, %v", created, err)
	}
	if err := q.Enqueue(ctx, queuedRun(id, 2)); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, queuedRun(id, 2)); !errors.Is(err, dispatcher.ErrAlreadyQueued) {
		t.Fatalf("second enqueue = %v, want ErrAlreadyQueued", err)
	}
	docs, err := client.Collection(runsCollection).Where(ticketIDField, "==", id).Documents(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("runs for %s = %d, want 1", id, len(docs))
	}
}

func TestUnmarkLetsTheNextDeliveryMarkAgain(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	session := fresh("session")
	t.Cleanup(func() {
		_, _ = client.Collection(dispatchCollection).Doc(webhookPrefix + session).Delete(context.Background())
	})
	markers := NewMarkers(client)

	if err := markers.Unmark(ctx, session); err != nil {
		t.Fatalf("unmarking an absent marker = %v", err)
	}
	if created, err := markers.Mark(ctx, session, "ABC-24", queueAt); err != nil || !created {
		t.Fatalf("mark = %v, %v", created, err)
	}
	if err := markers.Unmark(ctx, session); err != nil {
		t.Fatal(err)
	}
	if created, err := markers.Mark(ctx, session, "ABC-24", queueAt); err != nil || !created {
		t.Fatalf("mark after unmark = %v, %v", created, err)
	}
}

func TestMarkersReportAFailedWrite(t *testing.T) {
	_, client := queue(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	markers := NewMarkers(client)
	if created, err := markers.Mark(ctx, fresh("session"), "ABC-25", queueAt); err == nil || created {
		t.Fatalf("mark on a canceled context = %v, %v", created, err)
	}
	if err := markers.Unmark(ctx, fresh("session")); err == nil {
		t.Fatal("unmark on a canceled context succeeded")
	}
}
