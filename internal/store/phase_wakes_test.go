package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

// phaseWakeClient is an emulator client with no phase wake recorded, before
// and after the test, since every test shares the one document.
func phaseWakeClient(t *testing.T) *firestore.Client {
	t.Helper()
	_, client := queue(t)
	reset := func() { _, _ = client.Collection(dispatchCollection).Doc(phaseWakeDocID).Delete(context.Background()) }
	reset()
	t.Cleanup(reset)
	return client
}

func TestPhaseWakesClaimOncePerWindow(t *testing.T) {
	ctx, client := context.Background(), phaseWakeClient(t)
	w := NewPhaseWakes(client)
	at := time.Date(2026, 11, 19, 9, 0, 0, 123456789, time.UTC)
	for _, step := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"first", at, true},
		{"within the window", at.Add(59 * time.Second), false},
		{"a little behind", at.Add(-30 * time.Second), false},
		{"the window later", at.Add(time.Minute), true},
		{"far ahead of the last", at.Add(time.Hour), true},
		{"back from a future claim", at.Add(2 * time.Minute), true},
	} {
		claimed, err := w.Claim(ctx, step.at, time.Minute)
		if err != nil || claimed != step.want {
			t.Fatalf("%s: claimed %v, %v; want %v", step.name, claimed, err, step.want)
		}
	}
}

func TestPhaseWakesReleaseOnlyTheirOwnClaim(t *testing.T) {
	ctx, client := context.Background(), phaseWakeClient(t)
	w := NewPhaseWakes(client)
	at := time.Date(2026, 11, 19, 9, 0, 0, 123456789, time.UTC)

	if err := w.Release(ctx, at); err != nil {
		t.Fatalf("releasing with no claim = %v", err)
	}
	if claimed, err := w.Claim(ctx, at, time.Minute); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := w.Release(ctx, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := w.Claim(ctx, at.Add(2*time.Second), time.Minute); claimed {
		t.Fatal("releasing another time removed the claim")
	}
	if err := w.Release(ctx, at); err != nil {
		t.Fatal(err)
	}
	if claimed, err := w.Claim(ctx, at.Add(2*time.Second), time.Minute); err != nil || !claimed {
		t.Fatalf("claim after its release = %v, %v", claimed, err)
	}
}

func TestPhaseWakesReportAFailedClaim(t *testing.T) {
	_, client := queue(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := NewPhaseWakes(client)
	if claimed, err := w.Claim(ctx, time.Now(), time.Minute); err == nil || claimed {
		t.Fatalf("claim on a canceled context = %v, %v", claimed, err)
	}
}
