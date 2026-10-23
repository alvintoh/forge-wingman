package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestSplitModels(t *testing.T) {
	for in, want := range map[string][]string{
		"a/b":           {"a/b"},
		"a/b, c/d ,e/f": {"a/b", "c/d", "e/f"},
		"":              {runner.DefaultPlanModel()},
		"  ":            {runner.DefaultPlanModel()},
		"a/b,":          {"a/b", ""},
	} {
		if got := splitModels(in, []string{runner.DefaultPlanModel()}); !slices.Equal(got, want) {
			t.Errorf("splitModels(%q, default) = %q, want %q", in, got, want)
		}
	}
	if got := splitModels("", runner.DefaultReviewModels()); !slices.Equal(got, runner.DefaultReviewModels()) {
		t.Errorf("splitModels(\"\", review default) = %q, want the review default", got)
	}
}

func TestAgentRunnerRefusesAnImageNotPinnedByDigest(t *testing.T) {
	if _, _, _, err := agentRunner(context.Background(), agentSettings{Image: "golang:1.27.1"}, ".", nil); err == nil {
		t.Fatal("agentRunner accepted an image pinned by tag alone")
	}
}

func TestAgentRunnerRunsAgentsOnTheHostWithNoImage(t *testing.T) {
	harnesses := []runner.Harness{runner.OmpHarness{Bin: "omp"}}
	got, probe, _, err := agentRunner(context.Background(), agentSettings{}, t.TempDir(), harnesses)
	if err != nil || probe != nil || len(got) != 1 || got[0].(runner.OmpHarness).Container.Image != "" {
		t.Fatalf("agentRunner with no image = %v, probe %v, %v; want the harnesses unchanged and no probe", got, probe != nil, err)
	}
}

func TestAgentRunnerContainsHarnessesAndHandsBackTheProbe(t *testing.T) {
	image := "golang:1.27.1@sha256:" + strings.Repeat("a", 64)
	got, probe, _, err := agentRunner(context.Background(), agentSettings{Image: image}, ".", []runner.Harness{runner.OmpHarness{Bin: "omp"}})
	if err != nil || probe == nil || got[0].(runner.OmpHarness).Container.Image != image {
		t.Fatalf("agentRunner = %v, probe %v, %v; want the harness in the image's container and a probe", got, probe != nil, err)
	}
}

func TestAgentRunnerRefusesAnEgressSettingItCannotHonour(t *testing.T) {
	image := "golang:1.27.1@sha256:" + strings.Repeat("a", 64)
	for name, s := range map[string]agentSettings{
		"on with no image":  {Egress: "on"},
		"unknown value":     {Image: image, Egress: "yes"},
		"model with no api": {Image: image, Egress: "on", Plan: []string{"nowhere/model"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := agentRunner(context.Background(), s, ".", nil); err == nil {
				t.Fatal("agentRunner accepted the setting")
			}
		})
	}
}

// hostOnlyHarness cannot run in a container.
type hostOnlyHarness struct{ runner.Harness }

func (hostOnlyHarness) Name() string { return "host-only" }

func TestAgentRunnerWithEgressOnJoinsTheInternalNetworkAndStopRemovesIt(t *testing.T) {
	logFile := fakeDockerLog(t)
	s := agentSettings{Image: "golang:1.27.1@sha256:" + strings.Repeat("a", 64), Egress: "on", Plan: []string{"command-code/deepseek/deepseek-v4.1-flash"}}
	got, _, stop, err := agentRunner(context.Background(), s, ".", []runner.Harness{runner.OmpHarness{Bin: "omp"}})
	if err != nil {
		t.Fatal(err)
	}
	if net := got[0].(runner.OmpHarness).Container.Network; !strings.HasPrefix(net, "wingman-egress-net-") {
		t.Fatalf("container network %q, want the run's internal network", net)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := dockerLines(t, logFile)
	if n := len(lines); !strings.HasPrefix(lines[n-2], "rm --force ") || !strings.HasPrefix(lines[n-1], "network rm ") {
		t.Fatalf("docker calls %q, want the proxy then the network removed", lines)
	}
}

func TestAgentRunnerRemovesTheEgressWhenAHarnessCannotBeContained(t *testing.T) {
	logFile := fakeDockerLog(t)
	s := agentSettings{Image: "golang:1.27.1@sha256:" + strings.Repeat("a", 64), Egress: "on", Plan: []string{"command-code/deepseek/deepseek-v4.1-flash"}}
	if _, _, _, err := agentRunner(context.Background(), s, ".", []runner.Harness{hostOnlyHarness{}}); err == nil {
		t.Fatal("agentRunner contained a host-only harness")
	}
	if lines := dockerLines(t, logFile); !strings.HasPrefix(lines[len(lines)-1], "network rm ") {
		t.Fatalf("docker calls %q, want the egress removed after the failure", lines)
	}
}

func TestWithAgentsRemovesTheEgressAfterTheStageHoweverItEnds(t *testing.T) {
	image := "golang:1.27.1@sha256:" + strings.Repeat("a", 64)
	for name, stageErr := range map[string]error{"stage succeeds": nil, "stage fails": errors.New("stage failed")} {
		t.Run(name, func(t *testing.T) {
			logFile := fakeDockerLog(t)
			s := agentSettings{Image: image, Egress: "on", Plan: []string{"command-code/deepseek/deepseek-v4.1-flash"}}
			started, err := withAgents(context.Background(), slog.New(slog.DiscardHandler), s, ".", []runner.Harness{runner.OmpHarness{Bin: "omp"}},
				func([]runner.Harness, func(context.Context, string) error) error {
					for _, l := range dockerLines(t, logFile) {
						if strings.HasPrefix(l, "network rm ") {
							t.Error("the egress was removed before the stage ran")
						}
					}
					return stageErr
				})
			if !started || !errors.Is(err, stageErr) {
				t.Fatalf("withAgents = %v, %v; want started and the stage's error", started, err)
			}
			if lines := dockerLines(t, logFile); !strings.HasPrefix(lines[len(lines)-1], "network rm ") {
				t.Fatalf("docker calls %q, want the egress removed after the stage", lines)
			}
		})
	}
}

func TestWithAgentsDoesNotRunTheStageWhenTheAgentsCannotBeSetUp(t *testing.T) {
	ran := false
	started, err := withAgents(context.Background(), slog.New(slog.DiscardHandler), agentSettings{Image: "golang:1.27.1"}, ".", nil,
		func([]runner.Harness, func(context.Context, string) error) error { ran = true; return nil })
	if started || err == nil || ran {
		t.Fatalf("withAgents = %v, %v, ran %v; want a setup error and no stage", started, err, ran)
	}
}

func TestWithAgentsAllowsTheApiHostOfEveryModelTheStageNames(t *testing.T) {
	const flash, gemini = "command-code/deepseek/deepseek-v4.1-flash", "gemini/gemini-3.8-flash"
	for name, s := range map[string]agentSettings{
		"build model":  {Model: gemini, Plan: []string{flash}, Review: []string{flash}},
		"plan models":  {Model: flash, Plan: []string{flash, gemini}, Review: []string{flash}},
		"review model": {Model: flash, Plan: []string{flash}, Review: []string{flash, gemini}},
	} {
		t.Run(name, func(t *testing.T) {
			logFile := fakeDockerLog(t)
			s.Image, s.Egress = "golang:1.27.1@sha256:"+strings.Repeat("a", 64), "on"
			_, err := withAgents(context.Background(), slog.New(slog.DiscardHandler), s, ".", []runner.Harness{runner.OmpHarness{Bin: "omp"}},
				func([]runner.Harness, func(context.Context, string) error) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			var sidecar string
			for _, l := range dockerLines(t, logFile) {
				if strings.HasPrefix(l, "run --detach") {
					sidecar = l
				}
			}
			if !strings.Contains(sidecar, "-allow api.commandcode.ai,generativelanguage.googleapis.com") {
				t.Fatalf("sidecar %q, want both plans' hosts allowed", sidecar)
			}
		})
	}
}

func TestAgentSubcommandsRunTheirAgentInAContainerOnlyWithAnImage(t *testing.T) {
	const key = "model-key-value"
	image := "golang:1.27.1@sha256:" + strings.Repeat("a", 64)
	raw, err := json.Marshal(runner.PRReviewContext{PR: 7, HeadSHA: headSHA, Ticket: runner.Ticket{ID: "ABC-12", Title: "Add x", Body: "AC1"}, Diff: "+x"})
	if err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(t.TempDir(), "context.json")
	if err := os.WriteFile(contextPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// laguna is the build fallback now, and the checker refuses a review model that
	// is the build model or one of its fallbacks, so the fixtures use the configured list.
	reviewModel := runner.DefaultReviewModels()[0]
	subcommands := map[string]func(getenv func(string) string, args []string) error{
		"pr-review": func(getenv func(string) string, args []string) error {
			return prReview(context.Background(), quietLogger(), getenv,
				append([]string{"-context", contextPath, "-out", filepath.Join(t.TempDir(), "verdict.json"), "-models", reviewModel}, args...))
		},
		"review-smoke": func(getenv func(string) string, args []string) error {
			return reviewSmoke(context.Background(), quietLogger(), getenv, append([]string{"-review-models", reviewModel}, args...))
		},
		"plan-smoke": func(getenv func(string) string, args []string) error {
			return planSmoke(context.Background(), quietLogger(), getenv, args)
		},
	}
	for name, run := range subcommands {
		for _, contained := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s contained=%v", name, contained), func(t *testing.T) {
				dockerLog := fakeDockerLog(t)
				ompLog := filepath.Join(t.TempDir(), "omp.log")
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "omp"), []byte("#!/bin/sh\necho ran >> \""+ompLog+"\"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				summary := filepath.Join(t.TempDir(), "summary.md")
				env := map[string]string{"COMMANDCODE_API_KEY": key, "RUNNER_TEMP": t.TempDir(), "GITHUB_STEP_SUMMARY": summary}
				var args []string
				if contained {
					args = []string{"-agent-image", image}
				}
				_ = run(func(k string) string { return env[k] }, args)

				if info, err := os.Stat(summary); err != nil || info.Size() == 0 {
					t.Errorf("no summary written to GITHUB_STEP_SUMMARY: %v", err)
				}
				_, ompErr := os.Stat(ompLog)
				_, dockerErr := os.Stat(dockerLog)
				if contained {
					if ompErr == nil {
						t.Error("the agent ran on the host")
					}
					var runs []string
					for _, l := range dockerLines(t, dockerLog) {
						if strings.HasPrefix(l, "run ") {
							runs = append(runs, l)
						}
					}
					if len(runs) == 0 || !strings.Contains(runs[0], image) || !strings.Contains(runs[0], "--env COMMAND_CODE_API_KEY ") || strings.Contains(runs[0], key) {
						t.Fatalf("docker runs %q, want the agent in the image with the key by name only", runs)
					}
					return
				}
				if ompErr != nil || dockerErr == nil {
					t.Fatalf("omp ran on the host: %v, docker called: %v; want the host agent and no docker", ompErr == nil, dockerErr == nil)
				}
			})
		}
	}
}
