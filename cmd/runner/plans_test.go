package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func planEnv(account string) func(string) string {
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": "/tmp", "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": account, "GITHUB_REPOSITORY_OWNER": "octo"}
	return func(k string) string { return env[k] }
}

func TestPlanCommandsRefuseInvalidInputBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"a reply with no verdict": {[]string{"plan-reply", "-provider", "command-code", "-reply", "fine"}, "is not allowed, restricted or unconfirmed"},
		"a verdict with an impossible date": {[]string{"plan-verdict", "-provider", "command-code", "-verdict", "allowed",
			"-wording", "w", "-source", "https://v.example/terms", "-read-on", "2026-02-30"}, "yyyy-mm-dd"},
		"a definition with a malformed provider": {[]string{"plan-define", "-provider", "Open Code", "-name", "n", "-price-usd", "15"}, "is not a provider name"},
		"a definition with no price":             {[]string{"plan-define", "-provider", "command-code", "-name", "n"}, "not a positive amount"},
		"a definition with no billing":           {[]string{"plan-define", "-provider", "command-code", "-name", "n", "-price-usd", "15"}, "is not free, allowance or per-token"},
		"an opt-in naming a malformed provider":  {[]string{"plan-optin", "-provider", "Open Code"}, "is not a provider name"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, planEnv("octo"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestPlanCommandsRefuseAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, args := range [][]string{{"plan-list"}, {"plan-verdict", "-provider", "command-code"}, {"plan-optin", "-provider", "command-code"}} {
		err := run(context.Background(), logger, args, planEnv("work-account"))
		if !errors.Is(err, runner.ErrIdentityMismatch) {
			t.Errorf("%v: err = %v, want ErrIdentityMismatch", args, err)
		}
	}
}

func TestOpenPlansNeedsNoProviderForAListing(t *testing.T) {
	e := env{identity: runner.IdentityFromEnv(planEnv("octo"))}
	_, _, err := openPlans(context.Background(), e, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want it to get past the provider check and stop at the missing project", err)
	}
}

func TestPlanOptInPrivateRecordsOnlyThePrivateOptIn(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	provider := "plan-optin-private-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("provider_plans").Doc(provider).Delete(context.Background())
		_ = client.Close()
	})
	plans := store.NewPlans(client)
	if err := plans.PutDefinition(ctx, provider, providers.Definition{Name: "n", Billing: providers.BillingAllowance}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(ctx, logger, []string{"plan-optin", "-provider", provider, "-private"}, planEnv("octo")); err != nil {
		t.Fatal(err)
	}
	plan, _, err := plans.Plan(ctx, provider)
	if err != nil || !plan.PrivateOptIn || plan.OptedIn {
		t.Fatalf("plan %+v, err %v, want only the private opt-in recorded", plan, err)
	}
}

func TestTicketHandsTheBuildTheFreeTiersModelsOnAFreeTierClaim(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "ticket-free-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	rec := runner.NewRecord(runID, runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}, time.Now())
	rec.ModelLabels = runner.ModelLabels{Build: "p/paid"}
	rec.LastResort, rec.LastResortModels = true, runner.ModelLabels{Build: "p/free-a", Review: "p/free-b", Plan: []string{"p/free-a"}}
	if err := store.NewRecords(client).PutRecord(ctx, runID, rec); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": dir, "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": filepath.Join(dir, "output")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(ctx, logger, []string{"ticket", "-run-id", runID, "-out", filepath.Join(dir, "ticket.json")}, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(env["GITHUB_OUTPUT"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "override_model=p/free-a") || strings.Contains(string(out), "p/paid") {
		t.Fatalf("GITHUB_OUTPUT = %q, want the free tier's build model in place of the ticket's", out)
	}
}

func TestTicketRefusesAFreeTierClaimNamingNoFreeModel(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "ticket-nofree-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	rec := runner.NewRecord(runID, runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}, time.Now())
	rec.ModelLabels = runner.ModelLabels{Build: "p/paid"}
	rec.LastResort = true
	if err := store.NewRecords(client).PutRecord(ctx, runID, rec); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": dir, "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": filepath.Join(dir, "output")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = run(ctx, logger, []string{"ticket", "-run-id", runID, "-out", filepath.Join(dir, "ticket.json")}, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "names no free model") {
		t.Fatalf("err = %v, want the ticket refused rather than built on the paid model", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ticket.json")); statErr == nil {
		t.Fatal("ticket.json written for a free-tier claim with no free model")
	}
}
