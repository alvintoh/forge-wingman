package store

import (
	"context"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

var providersAt = time.Date(2026, 9, 27, 9, 15, 0, 0, time.UTC)

// providers returns a Providers over the emulator, with a cleanup that
// removes the doc for name.
func providers(t *testing.T, name string) (*Providers, *firestore.Client) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.Collection(providersCollection).Doc(name).Delete(context.Background())
		_ = client.Close()
	})
	return NewProviders(client), client
}

func TestRecordInfraStopHaltsOnceTheThresholdIsReached(t *testing.T) {
	name := fresh("prov-halt")
	p, _ := providers(t, name)
	ctx := context.Background()
	for i := 0; i < haltThreshold-1; i++ {
		if err := p.RecordInfraStop(ctx, name, providersAt); err != nil {
			t.Fatal(err)
		}
	}
	if halted, err := p.Halted(ctx, name); err != nil || halted {
		t.Fatalf("halted = %v, err %v, want still admitted below the threshold", halted, err)
	}
	if err := p.RecordInfraStop(ctx, name, providersAt); err != nil {
		t.Fatal(err)
	}
	if halted, err := p.Halted(ctx, name); err != nil || !halted {
		t.Fatalf("halted = %v, err %v, want halted at the threshold (AC4)", halted, err)
	}
}

func TestClearHaltAdmitsAHaltedProviderAgain(t *testing.T) {
	name := fresh("prov-clear")
	p, _ := providers(t, name)
	ctx := context.Background()
	for i := 0; i < haltThreshold; i++ {
		if err := p.RecordInfraStop(ctx, name, providersAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.ClearHalt(ctx, name); err != nil {
		t.Fatal(err)
	}
	if halted, err := p.Halted(ctx, name); err != nil || halted {
		t.Fatalf("halted = %v, err %v, want admitted again after ClearHalt (AC5)", halted, err)
	}
}

func TestRecordSuccessResetsAPartialStreakBeforeItHaltsAnything(t *testing.T) {
	name := fresh("prov-success")
	p, _ := providers(t, name)
	ctx := context.Background()
	for i := 0; i < haltThreshold-1; i++ {
		if err := p.RecordInfraStop(ctx, name, providersAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.RecordSuccess(ctx, name); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < haltThreshold-1; i++ {
		if err := p.RecordInfraStop(ctx, name, providersAt); err != nil {
			t.Fatal(err)
		}
	}
	if halted, err := p.Halted(ctx, name); err != nil || halted {
		t.Fatalf("halted = %v, err %v, want admitted — the streak reset on success, so only %d of %d stops are consecutive",
			halted, err, haltThreshold-1, haltThreshold)
	}
}

func TestHaltedReportsFalseForAProviderWithNoRecordedStop(t *testing.T) {
	name := fresh("prov-unknown")
	p, _ := providers(t, name)
	if halted, err := p.Halted(context.Background(), name); err != nil || halted {
		t.Fatalf("halted = %v, err %v, want a provider with no document treated as admitted", halted, err)
	}
}
