package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

// autoMergeSwitchesOver returns AutoMergeSwitches over the emulator with no
// document, and removes the document afterwards.
func autoMergeSwitchesOver(t *testing.T) (*AutoMergeSwitches, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewAutoMergeSwitches(client), client
}

func TestAutoMergeSwitchIsOnWhileNoneIsSet(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	on, err := a.Enabled(context.Background())
	if err != nil || !on {
		t.Fatalf("enabled = %v, %v, want on with no document", on, err)
	}
}

func TestAutoMergeSwitchSetIsReadBackAndRecordsWhen(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	if err := a.Set(ctx, false, noticeAt); err != nil {
		t.Fatal(err)
	}
	if on, err := a.Enabled(ctx); err != nil || on {
		t.Fatalf("enabled = %v, %v, want off", on, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data(); got[enabledField] != false || !got[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want enabled false updated at %v", got, noticeAt)
	}
	if err := a.Set(ctx, true, noticeAt); err != nil {
		t.Fatal(err)
	}
	if on, err := a.Enabled(ctx); err != nil || !on {
		t.Fatalf("enabled = %v, %v, want on after the switch back", on, err)
	}
}

func TestAutoMergeSwitchRefusesAValueThatIsNotABoolean(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	for name, doc := range map[string]map[string]any{
		"a string":         {enabledField: "off"},
		"no enabled field": {setAtField: noticeAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Set(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if on, err := a.Enabled(ctx); err == nil || on {
				t.Fatalf("Enabled = %v, %v, want an error so pr-meta holds", on, err)
			}
		})
	}
}
