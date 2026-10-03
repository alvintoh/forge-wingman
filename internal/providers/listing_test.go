package providers

import (
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/money"
)

func TestFlagsMarkWhatTheOwnerShouldLookAt(t *testing.T) {
	for name, tt := range map[string]struct {
		price money.Micros
		limit LimitBehaviour
		want  []Flag
	}{
		"inside the band at a hard stop":      {15 * money.Dollar, LimitHardStop, nil},
		"the lower edge is inside":            {10 * money.Dollar, LimitUnknown, nil},
		"the upper edge is inside":            {20 * money.Dollar, LimitUnknown, nil},
		"a cent above the band":               {20*money.Dollar + 10_000, LimitHardStop, []Flag{FlagOutsidePriceBand}},
		"below the band":                      {5 * money.Dollar, LimitHardStop, []Flag{FlagOutsidePriceBand}},
		"can spend past its allowance":        {15 * money.Dollar, LimitCanSpendPast, []Flag{FlagCanSpendPast}},
		"outside the band and spends past it": {30 * money.Dollar, LimitCanSpendPast, []Flag{FlagCanSpendPast, FlagOutsidePriceBand}},
	} {
		t.Run(name, func(t *testing.T) {
			got := Flags(Definition{MonthlyPrice: tt.price, LimitBehaviour: tt.limit})
			if len(got) != len(tt.want) {
				t.Fatalf("flags = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("flags = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestWriteTableShowsAPlanWithNoVerdictAsUnconfirmedAndEmptyCellsAsDashes(t *testing.T) {
	var b strings.Builder
	rows := []Listing{
		{Provider: "alpha", Plan: Plan{Definition: Definition{MonthlyPrice: 15 * money.Dollar, LimitBehaviour: LimitHardStop}}},
		{Provider: "beta", Plan: Plan{
			Definition: Definition{MonthlyPrice: 30 * money.Dollar, LimitBehaviour: LimitCanSpendPast},
			Verdict:    VerdictRestricted,
		}, FactsDate: "2026-09-27"},
	}
	if err := WriteTable(&b, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), b.String())
	}
	if got := strings.Fields(lines[1]); strings.Join(got, " ") != "alpha $15.00 hard-stop unconfirmed - -" {
		t.Fatalf("alpha row = %q", lines[1])
	}
	if got := strings.Fields(lines[2]); strings.Join(got, " ") != "beta $30.00 can-spend-past restricted 2026-09-27 can-spend-past,outside-price-band" {
		t.Fatalf("beta row = %q", lines[2])
	}
}
