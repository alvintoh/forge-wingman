// Package money is the exact-decimal cash representation every budget
// comparison in this product uses: Micros, millionths of a US dollar, so a
// sum of many small costs against a hard ceiling is int64 arithmetic and
// never round-trips through float64.
package money

import "math"

// Micros is a USD amount in millionths of a dollar — the fixed-point
// convention billing APIs use for sub-cent costs (a single model call can
// cost a fraction of a cent).
type Micros int64

// Dollar is one US dollar, expressed in Micros, so a ceiling can be written
// as e.g. 12 * money.Dollar rather than the equivalent magic number.
const Dollar Micros = 1_000_000

// FromUSD converts a dollar amount — as runner.Usage.Cost records it — to
// Micros, rounding to the nearest micro-dollar. This is the one place a
// recorded float64 cost may touch this product's budget arithmetic; every sum
// and comparison past it is int64.
func FromUSD(usd float64) Micros {
	return Micros(math.Round(usd * float64(Dollar)))
}

// USD renders m back to a float64 dollar amount, for logging and display
// only — never for a further comparison.
func (m Micros) USD() float64 { return float64(m) / float64(Dollar) }
