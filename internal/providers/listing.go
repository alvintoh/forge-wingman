package providers

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/alvintoh/forge-wingman/internal/money"
)

const (
	MinMonthlyPrice = 10 * money.Dollar
	MaxMonthlyPrice = 20 * money.Dollar
)

// Flag is something about a plan the owner should look at before relying on it.
type Flag string

const (
	FlagCanSpendPast     Flag = "can-spend-past"
	FlagOutsidePriceBand Flag = "outside-price-band"
)

// Flags lists what d warrants the owner's attention for.
func Flags(d Definition) []Flag {
	var flags []Flag
	if d.LimitBehaviour == LimitCanSpendPast {
		flags = append(flags, FlagCanSpendPast)
	}
	if d.MonthlyPrice < MinMonthlyPrice || d.MonthlyPrice > MaxMonthlyPrice {
		flags = append(flags, FlagOutsidePriceBand)
	}
	return flags
}

// Listing is one plan as plan-list shows it. FactsDate is the date of the
// latest refreshed facts, empty when none have been read.
type Listing struct {
	Provider  string
	Plan      Plan
	FactsDate string
}

// WriteTable writes the plans side by side, one row each.
func WriteTable(w io.Writer, rows []Listing) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PROVIDER\tPRICE\tLIMIT\tVERDICT\tFACTS\tFLAGS"); err != nil {
		return err
	}
	for _, r := range rows {
		verdict := string(r.Plan.Verdict)
		if verdict == "" {
			verdict = string(VerdictUnconfirmed)
		}
		var flags []string
		for _, f := range Flags(r.Plan.Definition) {
			flags = append(flags, string(f))
		}
		if _, err := fmt.Fprintf(tw, "%s\t$%.2f\t%s\t%s\t%s\t%s\n", r.Provider, r.Plan.Definition.MonthlyPrice.USD(),
			r.Plan.Definition.LimitBehaviour, verdict, orDash(r.FactsDate), orDash(strings.Join(flags, ","))); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
