package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// runsPerRepoOver returns RunsPerRepo over the emulator with no document, and
// removes the document afterwards.
func runsPerRepoOver(t *testing.T) (*RunsPerRepo, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(settingsCollection).Doc(runsPerRepoDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewRunsPerRepo(client), client
}

func TestRunsPerRepoDefaultsToFiveWhileNoneIsSet(t *testing.T) {
	r, _ := runsPerRepoOver(t)
	if n, err := r.Value(context.Background()); err != nil || n != 5 {
		t.Fatalf("value = %d, %v, want 5 with no document", n, err)
	}
}

func TestRunsPerRepoSetIsReadBackAndRecordsWhen(t *testing.T) {
	r, client := runsPerRepoOver(t)
	ctx := context.Background()
	for _, n := range []int{dispatcher.MinRunsPerRepo, 8, dispatcher.MaxRunsPerRepo} {
		if err := r.Set(ctx, n, noticeAt); err != nil {
			t.Fatal(err)
		}
		if got, err := r.Value(ctx); err != nil || got != n {
			t.Fatalf("value = %d, %v, want %d", got, err, n)
		}
	}
	snap, err := client.Collection(settingsCollection).Doc(runsPerRepoDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := snap.Data()
	if stored[runsPerRepoField] != int64(dispatcher.MaxRunsPerRepo) || !stored[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want %d set at %v", stored, dispatcher.MaxRunsPerRepo, noticeAt)
	}
}

func TestRunsPerRepoRefusesAValueOutsideTheSettingRange(t *testing.T) {
	r, client := runsPerRepoOver(t)
	ctx := context.Background()
	for _, n := range []int{0, -1, dispatcher.MaxRunsPerRepo + 1} {
		if err := r.Set(ctx, n, noticeAt); err == nil {
			t.Fatalf("Set(%d) succeeded, want it refused", n)
		}
	}
	if _, err := client.Collection(settingsCollection).Doc(runsPerRepoDocID).Get(ctx); err == nil {
		t.Fatal("a refused Set wrote the document")
	}
}

func TestRunsPerRepoRefusesAStoredValueItCannotRead(t *testing.T) {
	r, client := runsPerRepoOver(t)
	ctx := context.Background()
	write := func(t *testing.T, doc map[string]any) {
		t.Helper()
		if _, err := client.Collection(settingsCollection).Doc(runsPerRepoDocID).Set(ctx, doc); err != nil {
			t.Fatal(err)
		}
		if n, err := r.Value(ctx); err == nil {
			t.Fatalf("Value = %d, want an error so the dispatcher falls back to %d", n, dispatcher.DefaultRunsPerRepo)
		}
	}
	t.Run("a value that is not a number", func(t *testing.T) {
		write(t, map[string]any{runsPerRepoField: "8"})
	})
	t.Run("no value field", func(t *testing.T) {
		write(t, map[string]any{setAtField: noticeAt})
	})
	t.Run("a value of zero", func(t *testing.T) {
		write(t, map[string]any{runsPerRepoField: 0})
	})
	t.Run("a value past the range", func(t *testing.T) {
		write(t, map[string]any{runsPerRepoField: dispatcher.MaxRunsPerRepo + 1})
	})
}
