package main

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestLoadConfigReadsTheBoundaries(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{
		"GOOGLE_CLOUD_PROJECT": "forge-wingman",
		"LINEAR_DELEGATE":      "agent-1",
		"WINGMAN_REPOS":        "octo/scratch, AlvinToh/Forge-Wingman ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.project != "forge-wingman" || c.delegate != "agent-1" {
		t.Fatalf("config = %+v", c)
	}
	if !slices.Equal(c.repos, []string{"octo/scratch", "AlvinToh/Forge-Wingman"}) {
		t.Fatalf("repos = %v", c.repos)
	}
}

func TestLoadConfigRefusesAnUnconfiguredJob(t *testing.T) {
	full := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "forge-wingman",
		"LINEAR_DELEGATE":      "agent-1",
		"WINGMAN_REPOS":        "octo/scratch",
	}
	for _, unset := range []string{"GOOGLE_CLOUD_PROJECT", "LINEAR_DELEGATE"} {
		t.Run(unset, func(t *testing.T) {
			kv := map[string]string{}
			for k, v := range full {
				kv[k] = v
			}
			delete(kv, unset)
			_, err := loadConfig(envOf(kv))
			if err == nil || !strings.Contains(err.Error(), unset) {
				t.Fatalf("err = %v, want it to name %s", err, unset)
			}
		})
	}
	// An allowlist that names no repository would admit whatever a label names.
	for _, repos := range []string{"", "  ,  ", "octo"} {
		kv := map[string]string{}
		for k, v := range full {
			kv[k] = v
		}
		kv["WINGMAN_REPOS"] = repos
		if _, err := loadConfig(envOf(kv)); err == nil {
			t.Fatalf("WINGMAN_REPOS = %q was accepted", repos)
		}
	}
}

func TestRunRefusesBeforeReadingTheTokens(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(context.Background(), logger, envOf(map[string]string{})); err == nil {
		t.Fatal("polled with nothing configured")
	}
}
