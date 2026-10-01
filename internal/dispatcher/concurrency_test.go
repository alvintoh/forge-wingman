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
		"N never rises past the platform cap": {
			Concurrency{N: platformCap, Stable: 4}, Observation{Solo: solo, Loaded: solo}, platformCap, 0},
		"N above the platform cap is brought down to it": {
			Concurrency{N: platformCap + 5}, Observation{}, platformCap, 0},
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
