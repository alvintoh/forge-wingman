package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
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

func TestAutoMergeSettingIsOnAtTheDefaultBarWhileNoneIsSet(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	setting, err := a.Read(context.Background())
	if err != nil || !setting.Enabled || setting.BarPercent != DefaultBarPercent || setting.Escaped.PR != 0 {
		t.Fatalf("Read = %+v, %v, want on at %d%% holding nothing with no document", setting, err, DefaultBarPercent)
	}
}

func TestAutoMergeSwitchSetIsReadBackAndRecordsWhen(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	if err := a.SetEnabled(ctx, false, noticeAt); err != nil {
		t.Fatal(err)
	}
	if setting, err := a.Read(ctx); err != nil || setting.Enabled {
		t.Fatalf("Read = %+v, %v, want off", setting, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data(); got[enabledField] != false || !got[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want enabled false updated at %v", got, noticeAt)
	}
	if err := a.SetEnabled(ctx, true, noticeAt); err != nil {
		t.Fatal(err)
	}
	if setting, err := a.Read(ctx); err != nil || !setting.Enabled {
		t.Fatalf("Read = %+v, %v, want on after the switch back", setting, err)
	}
}

func TestAutoMergeBarIsSetAndReadBack(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	ctx := context.Background()
	if err := a.SetBar(ctx, 35, noticeAt); err != nil {
		t.Fatal(err)
	}
	if setting, err := a.Read(ctx); err != nil || setting.BarPercent != 35 {
		t.Fatalf("Read = %+v, %v, want the stored bar", setting, err)
	}
	if err := a.SetBar(ctx, 0, noticeAt); !errors.Is(err, ErrBarNotInRange) {
		t.Fatalf("SetBar(0) = %v, want it refused", err)
	}
	if err := a.SetBar(ctx, 101, noticeAt); !errors.Is(err, ErrBarNotInRange) {
		t.Fatalf("SetBar(101) = %v, want it refused", err)
	}
}

func TestAutoMergeSettingRefusesValuesItCannotRead(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	for name, tt := range map[string]struct {
		doc map[string]any
		bar bool
	}{
		"a switch that is a string":        {map[string]any{enabledField: "off"}, false},
		"a bar that is a string":           {map[string]any{enabledField: true, barField: "20"}, true},
		"a bar of zero":                    {map[string]any{enabledField: true, barField: int64(0)}, true},
		"a bar above 100":                  {map[string]any{enabledField: true, barField: int64(101)}, true},
		"a bar that is not a whole number": {map[string]any{enabledField: true, barField: 1.5}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Set(ctx, tt.doc); err != nil {
				t.Fatal(err)
			}
			setting, err := a.Read(ctx)
			if err == nil {
				t.Fatalf("Read = %+v, want an error so pr-meta holds", setting)
			}
			if errors.Is(err, ErrBarNotInRange) != tt.bar {
				t.Fatalf("err = %v, want ErrBarNotInRange = %v", err, tt.bar)
			}
		})
	}
}

// A document the escape latch wrote alone carries no switch, and reads as on:
// the latch must not make the settings unreadable.
func TestAutoMergeSettingReadsOnFromTheEscapeAlone(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	escape := dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341, By: 350, Reason: dispatcher.EscapeReverted, At: noticeAt}
	if _, err := a.RecordEscape(ctx, escape); err != nil {
		t.Fatal(err)
	}
	setting, err := a.Read(ctx)
	if err != nil || !setting.Enabled || setting.BarPercent != DefaultBarPercent || setting.Escaped != escape {
		t.Fatalf("Read = %+v, %v, want on at the default bar with the escape", setting, err)
	}
	if _, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Get(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMergeEscapeIsRecordedOnceAndClearedBySwitchingOn(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	ctx := context.Background()
	first := dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341, By: 350, Reason: dispatcher.EscapeReverted, At: noticeAt}
	second := dispatcher.MergeEscape{Repo: "octo/scratch", PR: 344, By: 360, Reason: dispatcher.EscapeFixed, At: noticeAt.Add(time.Hour)}
	if fresh, err := a.RecordEscape(ctx, first); err != nil || !fresh {
		t.Fatalf("RecordEscape = %v, %v, want the first recorded", fresh, err)
	}
	if fresh, err := a.RecordEscape(ctx, second); err != nil || fresh {
		t.Fatalf("RecordEscape = %v, %v, want a later escape to leave the first standing", fresh, err)
	}
	if setting, err := a.Read(ctx); err != nil || setting.Escaped != first {
		t.Fatalf("Read = %+v, %v, want the first escape", setting, err)
	}
	// Switching off leaves the latch: the switch is off, and the escape still
	// holds when it comes back on unacknowledged.
	if err := a.SetEnabled(ctx, false, noticeAt); err != nil {
		t.Fatal(err)
	}
	if setting, _ := a.Read(ctx); setting.Escaped != first {
		t.Fatalf("Read = %+v, want the escape kept while the switch is off", setting)
	}
	if err := a.SetEnabled(ctx, true, noticeAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if setting, err := a.Read(ctx); err != nil || !setting.Enabled || setting.Escaped.PR != 0 {
		t.Fatalf("Read = %+v, %v, want the escape cleared by switching on", setting, err)
	}
}
