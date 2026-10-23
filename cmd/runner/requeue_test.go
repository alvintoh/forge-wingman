package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestRequeueRefusesAMalformedRunIDBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	for _, id := range []string{"", "runs/x", "../x"} {
		err := run(context.Background(), logger, []string{"requeue", "-run-id", id}, func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "cannot name a record") {
			t.Errorf("id %q: err = %v, want refused before opening Firestore", id, err)
		}
	}
}

func TestRequeueRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), logger, []string{"requeue", "-run-id", "run-1"}, func(k string) string { return env[k] })
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
}

func TestRequeueCommandReturnsAnAbandonedRunToTheQueue(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "requeue-abandoned-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ref := client.Collection("runs").Doc(runID)
	t.Cleanup(func() {
		_, _ = ref.Delete(context.Background())
		_ = client.Close()
	})
	if _, err := ref.Set(ctx, map[string]any{"state": "abandoned", "claimed_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(ctx, logger, []string{"requeue", "-run-id", runID}, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	snap, err := ref.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data()["state"]; got != "queued" {
		t.Fatalf("state = %v, want queued", got)
	}
}
