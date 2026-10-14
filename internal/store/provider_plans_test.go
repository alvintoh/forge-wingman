package store

import (
	"slices"
	"testing"

	prov "github.com/alvintoh/forge-wingman/internal/providers"
)

// TestSortByDispatchPutsTheNamedPlansInOrderAndTheOthersLast asserts the
// providers view walks the plans the configuration names first, in that order —
// not the store's own provider order — and leaves a plan it does not name after
// them, keeping the store's order among those. Its rows arrive in the order the
// store reads them: by provider.
func TestSortByDispatchPutsTheNamedPlansInOrderAndTheOthersLast(t *testing.T) {
	rows := []prov.Listing{
		{Provider: "aaa"},
		{Provider: "command-code"},
		{Provider: "gemini"},
		{Provider: "zzz"},
	}

	sortByDispatch(rows, []string{"gemini", "command-code"})

	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.Provider)
	}
	if want := []string{"gemini", "command-code", "aaa", "zzz"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestWithConfiguredListsAPlanTheStoreHoldsNoRecordOf asserts a configured plan
// with no stored record still gets a row, holding no plan record, while a
// recorded plan is not listed twice.
func TestWithConfiguredListsAPlanTheStoreHoldsNoRecordOf(t *testing.T) {
	rows := []prov.Listing{{Provider: "gemini", Plan: prov.Plan{Verdict: prov.VerdictAllowed}}}

	got := withConfigured(rows, []string{"command-code", "gemini"})

	if len(got) != 2 || got[0].Provider != "gemini" || got[1].Provider != "command-code" {
		t.Fatalf("rows = %+v, want gemini then command-code", got)
	}
	if got[1].Plan.RecordedVerdict() != prov.VerdictUnconfirmed || got[1].Plan.Definition.MonthlyPrice != 0 {
		t.Errorf("unrecorded plan = %+v, want unpriced and unconfirmed", got[1].Plan)
	}
}
