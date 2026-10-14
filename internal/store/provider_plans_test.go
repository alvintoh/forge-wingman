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
