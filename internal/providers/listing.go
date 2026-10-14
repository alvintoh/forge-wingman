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

// HarnessLine is one plan-and-harness pair as the side-by-side view shows it:
// the harness, the model it serves, and the licence the harness's adapter
// records, empty for a harness this build does not register.
type HarnessLine struct {
	Harness string
	Model   string
	Licence string
}

// Listing is one plan as plan-list shows it. FactsDate is the date of the
// latest refreshed facts, empty when none have been read. Harnesses are the
// plan's pairs as the view lists them — open-source first.
type Listing struct {
	Provider  string
	Plan      Plan
	FactsDate string
	Harnesses []HarnessLine
}

// WriteTable writes the plans side by side, one row each.
func WriteTable(w io.Writer, rows []Listing) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PROVIDER\tPRICE\tLIMIT\tBILLING\tOPTED-IN\tVERDICT\tFACTS\tFLAGS\tHARNESSES"); err != nil {
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
		if _, err := fmt.Fprintf(tw, "%s\t$%.2f\t%s\t%s\t%t\t%s\t%s\t%s\t%s\n", r.Provider, r.Plan.Definition.MonthlyPrice.USD(),
			r.Plan.Definition.LimitBehaviour, orDash(string(r.Plan.Definition.Billing)), r.Plan.OptedIn, verdict, orDash(r.FactsDate), orDash(strings.Join(flags, ",")), orDash(harnessCell(r.Harnesses))); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// harnessCell renders a plan's pairs as the view shows them: each as
// harness:model, with the licence its adapter records in parentheses.
func harnessCell(lines []HarnessLine) string {
	cells := make([]string, 0, len(lines))
	for _, l := range lines {
		cell := l.Harness + ":" + l.Model
		if l.Licence != "" {
			cell += " (" + l.Licence + ")"
		}
		cells = append(cells, cell)
	}
	return strings.Join(cells, ",")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
