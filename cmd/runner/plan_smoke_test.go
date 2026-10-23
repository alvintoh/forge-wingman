package main

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
)

func TestSmokeModelsAreTheFirstAndLast(t *testing.T) {
	for _, tt := range []struct{ in, want []string }{
		{[]string{"a/1"}, []string{"a/1"}},
		{[]string{"a/1", "b/2"}, []string{"a/1", "b/2"}},
		{[]string{"a/1", "b/2", "c/3"}, []string{"a/1", "c/3"}},
	} {
		if got := smokeModels(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("smokeModels(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlanSmokeRefusesAnInvalidModelList(t *testing.T) {
	err := planSmoke(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(string) string { return "" }, []string{"-plan-models", "not a model"})
	if err == nil {
		t.Fatal("plan-smoke accepted a malformed model")
	}
}
