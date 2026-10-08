package runner

import (
	"fmt"
	"testing"
)

func TestSampledIsAFixedFunctionOfTheRunID(t *testing.T) {
	for id, want := range map[string]bool{"run-0": true, "run-1": true, "run-2": false} {
		if got := Sampled(id); got != want {
			t.Errorf("Sampled(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestSampledSelectsAboutOneRunInFive(t *testing.T) {
	n := 0
	for i := range 1000 {
		if Sampled(fmt.Sprintf("run-%d", i)) {
			n++
		}
	}
	if n < 150 || n > 250 {
		t.Fatalf("sampled %d of 1000 run ids, want 150-250", n)
	}
}
