package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func TestPRMetaRecordsWhyThePRWaitsForTheOwner(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "pr-meta-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	records := store.NewRecords(client)
	if err := records.PutRecord(ctx, runID, runner.NewRecord(runID, prRecord.Ticket(), time.Now())); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": dir, "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": filepath.Join(dir, "output")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	args := []string{"pr-meta", "-run-id", runID, "-repository", "octo/scratch", "-template", filepath.Join("..", "..", runner.DefaultPRTemplate)}
	if err := run(ctx, logger, args, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	got, err := records.GetRecord(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WaitReason != runner.WaitDraft {
		t.Fatalf("WaitReason = %q, want %q: a run with no build summary opens as a draft", got.WaitReason, runner.WaitDraft)
	}
}
