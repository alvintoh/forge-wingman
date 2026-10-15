package runner

import (
	"context"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dockerRunsOfBothContainers runs one checks gate set and one agent over a fake
// docker and returns the checks container's run call and the agent's.
func dockerRunsOfBothContainers(t *testing.T) (checks, agent []string) {
	t.Helper()
	logFile := fakeDocker(t)
	if gate, out, err := testContainerChecks(t).Run(context.Background(), initModule(t), nil); err != nil || gate != "" {
		t.Fatalf("checks Run = %q, %q, %v", gate, out, err)
	}
	checks = dockerCalls(t, logFile)[0]

	agentLog := fakeDocker(t)
	err := OmpAgent{Bin: "omp", Key: conformanceKey, Home: t.TempDir(), Model: ompTestModel, Container: testAgentContainer(t)}.
		Run(context.Background(), t.TempDir(), "", "prompt", "", io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return checks, dockerCalls(t, agentLog)[0]
}

func TestBothContainersCarryEveryHardeningFlag(t *testing.T) {
	checks, agent := dockerRunsOfBothContainers(t)
	user := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	for name, args := range map[string][]string{"checks": checks, "agent": agent} {
		for _, f := range []string{"--rm", "--init", "--read-only"} {
			if !slices.Contains(args, f) {
				t.Errorf("%s run lacks %s: %v", name, f, args)
			}
		}
		for flag, want := range map[string]string{"--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--tmpfs": "/tmp:exec", "--user": user} {
			if got := flagValues(args, flag); !slices.Equal(got, []string{want}) {
				t.Errorf("%s run %s %v, want [%s]", name, flag, got, want)
			}
		}
	}
}

func TestAFlagAddedToTheSharedListReachesBothContainers(t *testing.T) {
	orig := hardeningFlags
	t.Cleanup(func() { hardeningFlags = orig })
	hardeningFlags = append(slices.Clone(orig), "--label", "sentinel")

	checks, agent := dockerRunsOfBothContainers(t)
	for name, args := range map[string][]string{"checks": checks, "agent": agent} {
		if got := flagValues(args, "--label"); !slices.Equal(got, []string{"sentinel"}) {
			t.Errorf("%s run --label %v, want [sentinel]", name, got)
		}
	}
}

func TestHardenedRunArgs(t *testing.T) {
	user := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	base := hardenedRun{Name: "n", Mounts: []string{"m1", "m2"}, Workdir: "/w", Env: []string{"A=b", "C"}, Image: "img", Argv: []string{"cmd", "-x"}}
	flags := []string{"--read-only", "--tmpfs", "/tmp:exec", "--cap-drop", "ALL", "--security-opt", "no-new-privileges"}
	tail := []string{"--mount", "m1", "--mount", "m2", "--workdir", "/w", "--user", user, "--env", "A=b", "--env", "C", "img", "cmd", "-x"}

	interactive := base
	interactive.Interactive, interactive.Network = true, "net"
	tests := []struct {
		name string
		run  hardenedRun
		want []string
	}{
		{"neither", base, slices.Concat([]string{"run", "--rm", "--name", "n", "--init"}, flags, tail)},
		{"both", interactive, slices.Concat([]string{"run", "--rm", "--name", "n", "--init", "--interactive"}, flags, []string{"--network", "net"}, tail)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.run.args(); !slices.Equal(got, tt.want) {
				t.Fatalf("args =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

func TestHardenedRunCommandKillsTheContainerOnCancelAndKeepsSecretsOffTheClient(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_HANG", "1")
	t.Setenv("WINGMAN_TEST_SECRET", "s3cret")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	cmd := hardenedRunCommand(ctx, hardenedRun{Name: "wingman-test-1", Workdir: "/w", Image: "img"})
	if cmd.WaitDelay != containerKillTimeout {
		t.Errorf("WaitDelay = %s, want %s", cmd.WaitDelay, containerKillTimeout)
	}
	if slices.ContainsFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, "WINGMAN_TEST_SECRET=") }) {
		t.Error("the docker client inherits a non-docker secret")
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("Run succeeded past its deadline")
	}
	calls := dockerCalls(t, logFile)
	if len(calls) != 2 || !slices.Equal(calls[1], []string{"kill", "wingman-test-1"}) {
		t.Fatalf("docker ran %v, want the run then a kill of that container", calls)
	}
}

func TestContainerChecksKillAGateWhenTheParentContextIsCancelled(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_HANG", "1")
	c := testContainerChecks(t)
	dir := initModule(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for dockerCallsIfAny(logFile) == 0 {
			select {
			case <-t.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		cancel()
	}()

	start := time.Now()
	_, _, _ = c.Run(ctx, dir, nil)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want the hung gate killed", elapsed)
	}
	calls := dockerCalls(t, logFile)
	if len(calls) != 2 {
		t.Fatalf("docker ran %v, want the gate then a kill", calls)
	}
	if name := flagValues(calls[0], "--name"); len(name) != 1 || !slices.Equal(calls[1], []string{"kill", name[0]}) {
		t.Fatalf("docker %v after a run named %v, want that container killed", calls[1], name)
	}
}

// dockerCallsIfAny counts the complete lines fakeDocker has logged so far, 0 before its first call.
func dockerCallsIfAny(logFile string) int {
	raw, err := os.ReadFile(logFile)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "\n")
}

func TestCheckPinnedImage(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name, image string
		wantErr     bool
	}{
		{"tag only", "golang:1.27.1", true},
		{"short digest", "golang@sha256:abc", true},
		{"empty", "", true},
		{"full digest", "golang:1.27.1@sha256:" + digest, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPinnedImage("probe", tt.image)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkPinnedImage(%q) = %v, wantErr %v", tt.image, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "probe image") {
				t.Errorf("error %q does not name the label", err)
			}
		})
	}
}
