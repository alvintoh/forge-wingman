package store

import (
	"context"
	"testing"
)

// cdsOver returns CDs over the emulator with no document, and removes the
// document afterwards.
func cdsOver(t *testing.T) *CDs {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(dispatchCollection).Doc(cdDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewCDs(client)
}

func TestCDsRecordsAFlagPerRepository(t *testing.T) {
	c := cdsOver(t)
	ctx := context.Background()
	if flags, err := c.Recorded(ctx); err != nil || len(flags) != 0 {
		t.Fatalf("Recorded = %v, %v, want none recorded", flags, err)
	}
	if err := c.Set(ctx, "octo/scratch", true, noticeAt); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "octo/other", false, noticeAt); err != nil {
		t.Fatal(err)
	}
	flags, err := c.Recorded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 2 || !flags["octo/scratch"] || flags["octo/other"] {
		t.Fatalf("Recorded = %v, want scratch deploying and other not", flags)
	}
	// Recording one repository replaces only its own entry.
	if err := c.Set(ctx, "octo/scratch", false, noticeAt); err != nil {
		t.Fatal(err)
	}
	if flags, err = c.Recorded(ctx); err != nil || len(flags) != 2 || flags["octo/scratch"] {
		t.Fatalf("Recorded = %v, %v, want scratch replaced to false", flags, err)
	}
	if deploying, err := c.Deploying(ctx, "octo/scratch"); err != nil || deploying {
		t.Fatalf("Deploying(scratch) = %v, %v, want false", deploying, err)
	}
	if deploying, err := c.Deploying(ctx, "octo/absent"); err != nil || deploying {
		t.Fatalf("Deploying(absent) = %v, %v, want a repository with no entry read as false", deploying, err)
	}
}

func TestCDsMatchesARepositoryWhateverCaseItIsRecordedIn(t *testing.T) {
	c := cdsOver(t)
	ctx := context.Background()
	if err := c.Set(ctx, "octo/scratch", true, noticeAt); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "Octo/Scratch", false, noticeAt); err != nil {
		t.Fatal(err)
	}
	flags, err := c.Recorded(ctx)
	if err != nil || len(flags) != 1 || flags["octo/scratch"] {
		t.Fatalf("Recorded = %v, %v, want the one repository replaced to false", flags, err)
	}
	if deploying, err := c.Deploying(ctx, "OCTO/Scratch"); err != nil || deploying {
		t.Fatalf("Deploying = %v, %v, want either spelling to read the same entry", deploying, err)
	}
}
