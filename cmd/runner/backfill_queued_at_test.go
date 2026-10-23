package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestBackfillQueuedAtRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), logger, []string{"backfill-queued-at", "-dry-run"}, func(k string) string { return env[k] })
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
}

func TestBackfillQueuedAtStampsOnlyWithoutDryRun(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "backfill-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ref := client.Collection("runs").Doc(runID)
	t.Cleanup(func() {
		_, _ = ref.Delete(context.Background())
		_ = client.Close()
	})
	if _, err := ref.Set(ctx, map[string]any{"state": "queued", "started_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		args    []string
		stamped bool
	}{{[]string{"backfill-queued-at", "-dry-run"}, false}, {[]string{"backfill-queued-at"}, true}} {
		if err := run(ctx, logger, tc.args, func(k string) string { return env[k] }); err != nil {
			t.Fatal(err)
		}
		snap, err := ref.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, has := snap.Data()["queued_at"]; has != tc.stamped {
			t.Fatalf("%v: queued_at present = %v, want %v", tc.args, has, tc.stamped)
		}
	}
}
