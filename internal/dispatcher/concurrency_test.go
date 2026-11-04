package dispatcher

import (
	"testing"
	"time"
)

const platformCap = 20

func TestTuningFillsOnlyTheFieldsItLeavesOut(t *testing.T) {
	if got := (Tuning{}).OrDefault(); got != DefaultTuning {
		t.Fatalf("zero tuning = %+v, want the defaults", got)
	}
	got := Tuning{RiseWithin: 1.1}.OrDefault()
	want := Tuning{StableRuns: DefaultTuning.StableRuns, RiseWithin: 1.1, HalveBeyond: DefaultTuning.HalveBeyond}
	if got != want {
		t.Fatalf("partial tuning = %+v, want %+v", got, want)
	}
}

func TestNextConcurrency(t *testing.T) {
	solo := 10 * time.Minute
	for name, tt := range map[string]struct {
		cur    Concurrency
		obs    Observation
		wantN  int
		wantSt int
	}{
		"a stable median counts toward a rise": {
			Concurrency{N: 2, Stable: 1}, Observation{Solo: solo, Loaded: 12 * time.Minute}, 2, 2},
		"the fifth stable median raises N by one": {
			Concurrency{N: 2, Stable: 4}, Observation{Solo: solo, Loaded: 11 * time.Minute}, 3, 0},
		"a median exactly at the rise band is stable": {
			Concurrency{N: 2, Stable: 4}, Observation{Solo: 100 * time.Second, Loaded: 125 * time.Second}, 3, 0},
		"a median between the bands holds N and restarts the count": {
			Concurrency{N: 4, Stable: 3}, Observation{Solo: solo, Loaded: 14 * time.Minute}, 4, 0},
		"a median past the fall band halves N": {
			Concurrency{N: 6, Stable: 3}, Observation{Solo: solo, Loaded: 16 * time.Minute}, 3, 0},
		"a median exactly at the fall band holds N": {
			Concurrency{N: 4, Stable: 3}, Observation{Solo: 100 * time.Second, Loaded: 150 * time.Second}, 4, 0},
		"a rate-limit stop halves N": {
			Concurrency{N: 5, Stable: 3}, Observation{Solo: solo, Loaded: solo, RateLimited: true}, 2, 0},
		"halving never goes below one": {
			Concurrency{N: 1}, Observation{RateLimited: true}, 1, 0},
		"a rise from 15 stays at 15": {
			Concurrency{N: MaxRunsAtOnce, Stable: 4}, Observation{Solo: solo, Loaded: solo}, MaxRunsAtOnce, 0},
		"N above 15 is brought down to it": {
			Concurrency{N: platformCap}, Observation{}, MaxRunsAtOnce, 0},
		"an unset N reads as one": {
			Concurrency{}, Observation{Solo: solo, Loaded: solo}, 1, 1},
		"no solo baseline holds N": {
			Concurrency{N: 3, Stable: 2}, Observation{Loaded: solo}, 3, 2},
		"no loaded median holds N": {
			Concurrency{N: 3, Stable: 2}, Observation{Solo: solo}, 3, 2},
	} {
		t.Run(name, func(t *testing.T) {
			got := NextConcurrency(tt.cur, tt.obs, DefaultTuning, platformCap)
			if got.N != tt.wantN || got.Stable != tt.wantSt {
				t.Fatalf("got %+v, want N %d stable %d", got, tt.wantN, tt.wantSt)
			}
		})
	}
}

func TestNextConcurrencyIsHeldToAPlatformCapUnderFifteen(t *testing.T) {
	solo := 10 * time.Minute
	for name, cur := range map[string]Concurrency{
		"a rise from the cap": {N: 12, Stable: 4},
		"N above the cap":     {N: 15},
	} {
		t.Run(name, func(t *testing.T) {
			if got := NextConcurrency(cur, Observation{Solo: solo, Loaded: solo}, DefaultTuning, 12); got.N != 12 {
				t.Fatalf("got %+v, want N 12", got)
			}
		})
	}
}

func TestRecommend(t *testing.T) {
	solo := 10 * time.Minute
	stable := Observation{Solo: solo, Loaded: 12 * time.Minute}
	for name, tt := range map[string]struct {
		prev Recommendation
		n    int
		obs  Observation
		want Recommendation
	}{
		"a stable median counts toward one more": {
			Recommendation{N: 2, Stable: 2, Basis: BasisSteady}, 2, stable, Recommendation{N: 2, Stable: 3, Basis: BasisSteady}},
		"the fifth stable median recommends one more": {
			Recommendation{N: 2, Stable: 4, Basis: BasisSteady}, 2, stable, Recommendation{N: 3, Stable: 5, Basis: BasisRise}},
		"a stable median after a rise keeps recommending one more": {
			Recommendation{N: 3, Stable: 5, Basis: BasisRise}, 2, stable, Recommendation{N: 3, Stable: 5, Basis: BasisRise}},
		"a median between the bands recommends N and restarts the count": {
			Recommendation{N: 3, Stable: 5, Basis: BasisRise}, 2, Observation{Solo: solo, Loaded: 14 * time.Minute},
			Recommendation{N: 2, Basis: BasisSteady}},
		"a slow median recommends half": {
			Recommendation{N: 2, Stable: 3, Basis: BasisSteady}, 4, Observation{Solo: solo, Loaded: 16 * time.Minute},
			Recommendation{N: 2, Basis: BasisSlow}},
		"a rate-limit stop recommends half": {
			Recommendation{}, 5, Observation{RateLimited: true}, Recommendation{N: 2, Basis: BasisRateLimited}},
		"halving one recommends one": {
			Recommendation{}, 1, Observation{RateLimited: true}, Recommendation{N: 1, Basis: BasisSteady}},
		"no medians leave the recommendation as it was": {
			Recommendation{N: 1, Basis: BasisSlow}, 2, Observation{Solo: solo}, Recommendation{N: 1, Basis: BasisSlow}},
		"15 holds the recommendation at N": {
			Recommendation{N: MaxRunsAtOnce, Stable: 4, Basis: BasisSteady}, MaxRunsAtOnce, stable,
			Recommendation{N: MaxRunsAtOnce, Basis: BasisSteady}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Recommend(tt.prev, tt.n, tt.obs, DefaultTuning, platformCap); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
