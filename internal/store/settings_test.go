package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// settingsOver returns Settings over the emulator with no document, and removes
// the document afterwards.
func settingsOver(t *testing.T) (*Settings, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(runsPerRepoDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewSettings(client), client
}

func TestRunsPerRepoIsTheDefaultWhileNoneIsSet(t *testing.T) {
	s, _ := settingsOver(t)
	got, err := s.RunsPerRepo(context.Background())
	if err != nil || got != dispatcher.DefaultLimits.RepoCap {
		t.Fatalf("RunsPerRepo = %d, %v, want the default %d", got, err, dispatcher.DefaultLimits.RepoCap)
	}
}

func TestRunsPerRepoSetIsReadBackAndRecordsWhen(t *testing.T) {
	s, client := settingsOver(t)
	ctx := context.Background()
	if err := s.SetRunsPerRepo(ctx, 8, noticeAt); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RunsPerRepo(ctx); err != nil || got != 8 {
		t.Fatalf("RunsPerRepo = %d, %v, want the saved 8", got, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(runsPerRepoDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data(); got[runsPerRepoField] != int64(8) || !got[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want the cap 8 set at %v", got, noticeAt)
	}
}

func TestRunsPerRepoRefusesAValueOutsideTheRange(t *testing.T) {
	s, _ := settingsOver(t)
	ctx := context.Background()
	for _, n := range []int{0, dispatcher.MaxRunsPerRepo + 1} {
		if err := s.SetRunsPerRepo(ctx, n, noticeAt); err == nil {
			t.Fatalf("SetRunsPerRepo(%d) = nil, want a refusal", n)
		}
	}
	if got, err := s.RunsPerRepo(ctx); err != nil || got != dispatcher.DefaultLimits.RepoCap {
		t.Fatalf("RunsPerRepo = %d, %v, want the default after refused writes", got, err)
	}
}

func TestRunsPerRepoRefusesAStoredValueItCannotUse(t *testing.T) {
	s, client := settingsOver(t)
	ctx := context.Background()
	doc := client.Collection(dispatchCollection).Doc(runsPerRepoDocID)
	for _, value := range []any{0, dispatcher.MaxRunsPerRepo + 1, "many"} {
		if _, err := doc.Set(ctx, map[string]any{runsPerRepoField: value}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.RunsPerRepo(ctx); err == nil || got != 0 {
			t.Fatalf("RunsPerRepo with %v stored = %d, %v, want an error", value, got, err)
		}
	}
}
