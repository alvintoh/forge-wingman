package store

import (
	"context"
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

func TestAutoMergeSettingIsOnWhileNoneIsSet(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	setting, err := a.Read(context.Background())
	if err != nil || !setting.Enabled || setting.Escaped.PR != 0 {
		t.Fatalf("Read = %+v, %v, want on holding nothing with no document", setting, err)
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

// An empty document carries nothing a renamed key could have lost, so it reads as
// no document does.
func TestAutoMergeSettingReadsOnFromAnEmptyDocument(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	if _, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Set(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if setting, err := a.Read(ctx); err != nil || !setting.Enabled {
		t.Fatalf("Read = %+v, %v, want on from an empty document", setting, err)
	}
}

// A bar stored before this setting lost it is left unread: the switch is the
// only thing that gates auto-merge, and a stale bar must not make the settings
// unreadable.
func TestAutoMergeSettingIgnoresAStoredBar(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	if _, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Set(ctx, map[string]any{
		enabledField: false, "bar": int64(1), setAtField: noticeAt,
	}); err != nil {
		t.Fatal(err)
	}
	setting, err := a.Read(ctx)
	if err != nil || setting.Enabled {
		t.Fatalf("Read = %+v, %v, want off with the stored bar ignored", setting, err)
	}
}

func TestAutoMergeSettingRefusesValuesItCannotRead(t *testing.T) {
	a, client := autoMergeSwitchesOver(t)
	ctx := context.Background()
	for name, doc := range map[string]map[string]any{
		"a switch that is a string": {enabledField: "off"},
		"no switch beside set_at":   {setAtField: noticeAt},
		"no switch beside a bar":    {"bar": int64(20)},
		"no switch beside a latch":  {escapedPRField: int64(7)},
		"a renamed switch":          {"enable": true, setAtField: noticeAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID).Set(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if setting, err := a.Read(ctx); err == nil {
				t.Fatalf("Read = %+v, want an error so pr-meta holds", setting)
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
	if err != nil || !setting.Enabled || setting.Escaped != escape {
		t.Fatalf("Read = %+v, %v, want on with the escape", setting, err)
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

// The owner switching on acknowledges the escaped PR: a poll reads the same revert
// again, and it must not latch a second time, while an escape of another PR still
// does.
func TestAnAcknowledgedEscapeStaysClearedAcrossPolls(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	ctx := context.Background()
	escaped := dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341, By: 350, Reason: dispatcher.EscapeReverted, At: noticeAt}
	if fresh, err := a.RecordEscape(ctx, escaped); err != nil || !fresh {
		t.Fatalf("RecordEscape = %v, %v, want the first recorded", fresh, err)
	}
	if err := a.SetEnabled(ctx, true, noticeAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if fresh, err := a.RecordEscape(ctx, escaped); err != nil || fresh {
		t.Fatalf("RecordEscape = %v, %v, want the acknowledged PR to stay cleared", fresh, err)
	}
	if setting, err := a.Read(ctx); err != nil || setting.Escaped.PR != 0 || !setting.Enabled {
		t.Fatalf("Read = %+v, %v, want nothing latched after the re-read", setting, err)
	}
	state, err := a.State(ctx)
	if err != nil || state.Latched || !state.Settled("octo/scratch", 341) || state.Settled("octo/scratch", 342) {
		t.Fatalf("State = %+v, %v, want only PR 341 settled", state, err)
	}
	next := dispatcher.MergeEscape{Repo: "octo/scratch", PR: 342, By: 351, Reason: dispatcher.EscapeFixed, At: noticeAt.Add(2 * time.Hour)}
	if fresh, err := a.RecordEscape(ctx, next); err != nil || !fresh {
		t.Fatalf("RecordEscape = %v, %v, want a new escape to latch again", fresh, err)
	}
}

// Switching on with nothing latched acknowledges nothing, so a first escape
// reported after it still latches.
func TestSwitchingOnWithNothingLatchedClearsNothing(t *testing.T) {
	a, _ := autoMergeSwitchesOver(t)
	ctx := context.Background()
	if err := a.SetEnabled(ctx, true, noticeAt); err != nil {
		t.Fatal(err)
	}
	escaped := dispatcher.MergeEscape{Repo: "octo/scratch", PR: 341, By: 350, Reason: dispatcher.EscapeReverted, At: noticeAt}
	if fresh, err := a.RecordEscape(ctx, escaped); err != nil || !fresh {
		t.Fatalf("RecordEscape = %v, %v, want it to latch", fresh, err)
	}
}
