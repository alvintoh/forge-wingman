package money

import "testing"

func TestFromUSDRoundsToTheNearestMicro(t *testing.T) {
	for _, tt := range []struct {
		usd  float64
		want Micros
	}{
		{0, 0},
		{1, 1_000_000},
		{0.5, 500_000},
		{0.0000004, 0},
		{0.0000006, 1},
		{12.3456785, 12_345_679}, // rounds half away from zero at the micro boundary
	} {
		if got := FromUSD(tt.usd); got != tt.want {
			t.Errorf("FromUSD(%v) = %d, want %d", tt.usd, got, tt.want)
		}
	}
}

func TestUSDRoundTripsFromUSD(t *testing.T) {
	if got := (12 * Dollar).USD(); got != 12 {
		t.Fatalf("USD() = %v, want 12", got)
	}
}
