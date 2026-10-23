package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestAgentProbeNeedsAnImageAndACommand(t *testing.T) {
	for _, args := range [][]string{{"--", "sh"}, {"-agent-image", "x@sha256:" + strings.Repeat("a", 64)}} {
		if err := agentProbe(context.Background(), io.Discard, io.Discard, args); err == nil {
			t.Errorf("agentProbe %v succeeded, want a usage error", args)
		}
	}
}
