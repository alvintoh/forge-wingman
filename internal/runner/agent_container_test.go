package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// containedAgent is a runnable agent configured for a container, over fakeDocker.
type containedAgent struct {
	name    string
	harness func(c AgentContainer, home string) Harness
	key     string
	// keyEnv is the variable the harness gives the model key under.
	keyEnv string
	bin    string
	// env is the variables the harness runs under, given its home.
	env func(home string) []string
}

func containedAgents() []containedAgent {
	return []containedAgent{
		{"omp", func(c AgentContainer, home string) Harness {
			return OmpHarness{Bin: "/host/bin/omp", Key: conformanceKey, Home: home, Container: c}
		}, conformanceKey, "COMMAND_CODE_API_KEY", "omp", func(home string) []string { return []string{"HOME=" + home} }},
		{"opencode", func(c AgentContainer, home string) Harness {
			return OpencodeHarness{Bin: "/host/bin/opencode", Key: conformanceKey, Home: home, Container: c}
		}, conformanceKey, "COMMAND_CODE_API_KEY", "opencode", func(home string) []string {
			return []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "XDG_DATA_HOME=" + home + "/.local/share",
				"XDG_CACHE_HOME=" + home + "/.cache", "XDG_STATE_HOME=" + home + "/.local/state"}
		}},
	}
}

func testAgentContainer(t *testing.T) AgentContainer {
	t.Helper()
	return AgentContainer{Image: pinnedChecksImage(t), GitCommonDir: t.TempDir()}
}

func TestAgentContainerRunsEachAgentHardenedWithOnlyItsMountsAndTheKeyByName(t *testing.T) {
	for _, tt := range containedAgents() {
		t.Run(tt.name, func(t *testing.T) {
			logFile := fakeDocker(t)
			c := testAgentContainer(t)
			dir, home := t.TempDir(), t.TempDir()

			agent := tt.harness(c, home).Agent(ProfileBuild, ompTestModel)
			if err := agent.Run(context.Background(), dir, "", "prompt", "", io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			calls := dockerCalls(t, logFile)
			if len(calls) != 1 {
				t.Fatalf("docker ran %v, want one container", calls)
			}
			args := calls[0]
			for _, f := range []string{"run", "--rm", "--init", "--interactive", "--read-only"} {
				if !slices.Contains(args, f) {
					t.Errorf("docker run lacks %s: %v", f, args)
				}
			}
			for _, f := range []string{"-v", "--volume", "--env-file", "--privileged", "--network", "--net", "-e"} {
				if slices.Contains(args, f) {
					t.Errorf("docker run carries %s: %v", f, args)
				}
			}
			if got := flagValues(args, "--cap-drop"); !slices.Equal(got, []string{"ALL"}) {
				t.Errorf("--cap-drop %v, want ALL", got)
			}
			if got := flagValues(args, "--security-opt"); !slices.Equal(got, []string{"no-new-privileges"}) {
				t.Errorf("--security-opt %v, want no-new-privileges", got)
			}
			wantMounts := []string{
				"type=bind,src=" + dir + ",dst=" + dir,
				"type=bind,src=" + home + ",dst=" + home,
				"type=bind,src=" + c.GitCommonDir + ",dst=" + c.GitCommonDir + ",readonly",
			}
			if got := flagValues(args, "--mount"); !slices.Equal(got, wantMounts) {
				t.Errorf("mounts = %v, want only %v", got, wantMounts)
			}
			if got := flagValues(args, "--workdir"); !slices.Equal(got, []string{dir}) {
				t.Errorf("--workdir %v, want the worktree", got)
			}
			at := slices.Index(args, c.Image)
			if at < 0 || args[at+1] != tt.bin {
				t.Fatalf("docker run %v, want the image then %s by its base name", args, tt.bin)
			}
			if strings.Contains(strings.Join(args, "\x1f"), tt.key) {
				t.Errorf("the model key is on the docker command line: %v", args)
			}
			if !slices.Contains(flagValues(args, "--env"), tt.keyEnv) {
				t.Errorf("env %v, want %s inherited by name", flagValues(args, "--env"), tt.keyEnv)
			}
			for _, want := range tt.env(home) {
				if !slices.Contains(flagValues(args, "--env"), want) {
					t.Errorf("env %v, want %s, which the agent needs to write under its mounted home", flagValues(args, "--env"), want)
				}
			}
			dockerEnv, err := os.ReadFile(logFile + ".env")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(dockerEnv), tt.keyEnv+"="+tt.key+"\n") {
				t.Errorf("the docker client's environment lacks %s, so the container cannot inherit it", tt.keyEnv)
			}
		})
	}
}

func TestAgentCommandRunsTheAgentItselfOnTheHostWithNoImage(t *testing.T) {
	logFile := fakeDocker(t)
	cmd, err := agentCommand(context.Background(), AgentContainer{}, agentSpec{
		Bin: "/host/bin/omp", Args: []string{"-p"}, Dir: "/work", Env: []string{"HOME=/h"}, Secret: "K=v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/host/bin/omp" || !slices.Equal(cmd.Args, []string{"/host/bin/omp", "-p"}) || cmd.Dir != "/work" {
		t.Errorf("cmd = %v in %q, want the host binary run as given", cmd.Args, cmd.Dir)
	}
	if env := cmd.Env; env[len(env)-2] != "HOME=/h" || env[len(env)-1] != "K=v" {
		t.Errorf("env tail = %v, want the harness's variables then the key", env[len(env)-2:])
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("docker was invoked with no image: %v", err)
	}
}

func TestAgentContainerKillsTheContainerWhenTheRunIsCancelled(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_HANG", "1")
	c := testAgentContainer(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	err := OmpAgent{Bin: "omp", Key: conformanceKey, Home: t.TempDir(), Model: ompTestModel, Container: c}.
		Run(ctx, t.TempDir(), "", "prompt", "", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Run succeeded past its deadline")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want the hung container killed", elapsed)
	}
	calls := dockerCalls(t, logFile)
	if len(calls) != 2 {
		t.Fatalf("docker ran %v, want the run then a kill", calls)
	}
	if name := flagValues(calls[0], "--name"); len(name) != 1 || !slices.Equal(calls[1], []string{"kill", name[0]}) {
		t.Fatalf("docker %v after a run named %v, want that container killed", calls[1], name)
	}
}

func TestAgentContainerValidate(t *testing.T) {
	good := testAgentContainer(t)
	tests := []struct {
		name   string
		mutate func(*AgentContainer)
	}{
		{"no image", func(c *AgentContainer) { c.Image = "" }},
		{"image pinned by tag", func(c *AgentContainer) { c.Image = "golang:1.27.1" }},
		{"relative git dir", func(c *AgentContainer) { c.GitCommonDir = "repo/.git" }},
		{"comma in git dir", func(c *AgentContainer) { c.GitCommonDir = "/a,readonly=false" }},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid configuration: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good
			tt.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("Validate accepted %+v", c)
			}
		})
	}
}

func TestAgentContainerRefusesAMountPathADockerMountCannotCarry(t *testing.T) {
	fakeDocker(t)
	c := testAgentContainer(t)
	err := OmpAgent{Bin: "omp", Key: conformanceKey, Home: filepath.Join(t.TempDir(), "a,b"), Model: ompTestModel, Container: c}.
		Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Run accepted a harness home with a comma")
	}
}

func TestAgentContainerProbeRefusesAGitDirADockerMountCannotCarry(t *testing.T) {
	fakeDocker(t)
	c := testAgentContainer(t)
	c.GitCommonDir = "/a,b"
	_, probe, err := WithAgentContainer([]Harness{OmpHarness{Bin: "omp", Home: t.TempDir()}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), t.TempDir()); err == nil {
		t.Fatal("the probe accepted a git common dir with a comma")
	}
}

func TestAgentContainerProbeStopsAContainerThatNeverStarts(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_HANG", "1")
	old := agentProbeTimeout
	agentProbeTimeout = 500 * time.Millisecond
	t.Cleanup(func() { agentProbeTimeout = old })

	_, probe, err := WithAgentContainer([]Harness{OmpHarness{Bin: "omp", Home: t.TempDir()}}, testAgentContainer(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), t.TempDir()); err == nil {
		t.Fatal("the probe succeeded on a container that hung")
	}
	if calls := dockerCalls(t, logFile); len(calls) != 2 || calls[1][0] != "kill" {
		t.Fatalf("docker ran %v, want the run then a kill", calls)
	}
}

func TestAgentContainerProbeRunsEveryHarnessVersionOverItsHome(t *testing.T) {
	logFile := fakeDocker(t)
	c, dir := testAgentContainer(t), t.TempDir()
	omp := OmpHarness{Bin: "omp", Home: filepath.Join(t.TempDir(), "omp-home")}
	oc := OpencodeHarness{Bin: "opencode", Home: filepath.Join(t.TempDir(), "opencode-home")}
	_, probe, err := WithAgentContainer([]Harness{omp, oc}, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	calls := dockerCalls(t, logFile)
	if len(calls) != 2 {
		t.Fatalf("docker ran %v, want one container per harness", calls)
	}
	// The probe runs the checks in the order the harnesses are given.
	tests := []struct {
		bin  string
		home string
	}{
		{"omp", omp.Home},
		{"opencode", oc.Home},
	}
	for i, tt := range tests {
		call := calls[i]
		if at := slices.Index(call, c.Image); at < 0 || !slices.Equal(call[at+1:], []string{tt.bin, "--version"}) {
			t.Errorf("docker ran %v, want %s --version in the image", call, tt.bin)
		}
		wantMounts := []string{
			"type=bind,src=" + dir + ",dst=" + dir,
			"type=bind,src=" + tt.home + ",dst=" + tt.home,
			"type=bind,src=" + c.GitCommonDir + ",dst=" + c.GitCommonDir + ",readonly",
		}
		if got := flagValues(call, "--mount"); !slices.Equal(got, wantMounts) {
			t.Errorf("%s mounts = %v, want %v", tt.bin, got, wantMounts)
		}
		if _, err := os.Stat(tt.home); err != nil {
			t.Errorf("%s home %s is not there for the mount: %v", tt.bin, tt.home, err)
		}
	}
}

func TestAgentContainerProbeSetsTheEnvironmentARealRunSets(t *testing.T) {
	for _, tt := range containedAgents() {
		t.Run(tt.name, func(t *testing.T) {
			logFile := fakeDocker(t)
			c, dir, home := testAgentContainer(t), t.TempDir(), t.TempDir()
			h := tt.harness(c, home)

			if err := h.Agent(ProfileBuild, ompTestModel).Run(context.Background(), dir, "", "prompt", "", io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			_, probe, err := WithAgentContainer([]Harness{h}, c)
			if err != nil {
				t.Fatal(err)
			}
			if err := probe(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
			calls := dockerCalls(t, logFile)
			if len(calls) != 2 {
				t.Fatalf("docker ran %v, want the agent then the probe", calls)
			}
			run, probed := calls[0], calls[1]
			if got, want := namedEnv(flagValues(probed, "--env")), namedEnv(flagValues(run, "--env")); !slices.Equal(got, want) {
				t.Errorf("probe env %v, want the run's %v", got, want)
			}
			if got, want := flagValues(probed, "--mount"), flagValues(run, "--mount"); !slices.Equal(got, want) {
				t.Errorf("probe mounts %v, want the run's %v", got, want)
			}
		})
	}
}

// namedEnv is the NAME=value entries of --env flags, without the bare names a
// run inherits its key by: a probe carries the same values and no key.
func namedEnv(vals []string) []string {
	var out []string
	for _, v := range vals {
		if strings.ContainsRune(v, '=') {
			out = append(out, v)
		}
	}
	return out
}

func TestAgentContainerProbeCarriesNoModelKey(t *testing.T) {
	for _, tt := range containedAgents() {
		t.Run(tt.name, func(t *testing.T) {
			logFile := fakeDocker(t)
			c := testAgentContainer(t)
			_, probe, err := WithAgentContainer([]Harness{tt.harness(c, t.TempDir())}, c)
			if err != nil {
				t.Fatal(err)
			}
			if err := probe(context.Background(), t.TempDir()); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{logFile, logFile + ".env"} {
				raw, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), tt.key) {
					t.Errorf("the probe put the model key in %s", filepath.Base(f))
				}
			}
		})
	}
}

func TestAgentContainerProbeReportsTheHarnessTheImageCannotStart(t *testing.T) {
	tests := []struct {
		name    string
		missing string
	}{
		{"the default harness", "omp"},
		{"the fallback harness", "opencode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeDocker(t)
			t.Setenv("DOCKER_FAKE_FAIL", tt.missing)
			t.Setenv("DOCKER_FAKE_CODE", "127")
			_, probe, err := WithAgentContainer([]Harness{
				OmpHarness{Bin: "omp", Home: t.TempDir()},
				OpencodeHarness{Bin: "opencode", Home: t.TempDir()},
			}, testAgentContainer(t))
			if err != nil {
				t.Fatal(err)
			}
			err = probe(context.Background(), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("the probe = %v, want an error carrying the container's output", err)
			}
			if !strings.Contains(err.Error(), tt.missing) {
				t.Errorf("the probe = %v, want it to name %s", err, tt.missing)
			}
		})
	}
}

func TestBuildStopsAtAgentStartWhenTheImageLacksAHarness(t *testing.T) {
	fakeDocker(t)
	t.Setenv("DOCKER_FAKE_FAIL", "opencode")
	t.Setenv("DOCKER_FAKE_CODE", "127")
	agent := &fakeAgent{}
	deps, _, reported := testDeps(validObjects(), agent)
	_, probe, err := WithAgentContainer([]Harness{
		OmpHarness{Bin: "omp", Home: t.TempDir()},
		OpencodeHarness{Bin: "opencode", Home: t.TempDir()},
	}, testAgentContainer(t))
	if err != nil {
		t.Fatal(err)
	}
	deps.AgentProbe = probe

	if _, err := Build(context.Background(), deps, testConfig(t, initRepo(t))); err == nil {
		t.Fatal("Build succeeded with an image that cannot run opencode")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeInfraFailure || rec.StopReason != StopAgentContainer || rec.Phase != PhaseAgentStart || agent.calls != 0 {
		t.Fatalf("record = %s/%s at %s, %d agent runs; want an infra failure at agent-start with no agent run", rec.Outcome, rec.StopReason, rec.Phase, agent.calls)
	}
}

func TestBuildRecordsTheAgentContainerStartAsATimedPhase(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	var probed string
	deps.AgentProbe = func(_ context.Context, dir string) error { probed = dir; return nil }
	if _, err := Build(context.Background(), deps, testConfig(t, initRepo(t))); err != nil {
		t.Fatal(err)
	}
	rec := reported.last(t)
	if _, ok := rec.DurationsMS[string(PhaseAgentStart)]; !ok || probed == "" {
		t.Fatalf("durations = %v, probed %q, want the agent-start phase timed over the worktree", rec.DurationsMS, probed)
	}
}

func TestBuildTimesNoAgentStartWithoutAProbe(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	if _, err := Build(context.Background(), deps, testConfig(t, initRepo(t))); err != nil {
		t.Fatal(err)
	}
	if _, ok := reported.last(t).DurationsMS[string(PhaseAgentStart)]; ok {
		t.Fatal("a build with no agent container timed an agent-start phase")
	}
}

func TestBuildStopsBeforeAnyAgentRunsWhenTheAgentContainerWillNotStart(t *testing.T) {
	agent := &fakeAgent{}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.AgentProbe = func(context.Context, string) error { return io.ErrUnexpectedEOF }
	if _, err := Build(context.Background(), deps, testConfig(t, initRepo(t))); err == nil {
		t.Fatal("Build succeeded with an agent container that would not start")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeInfraFailure || rec.StopReason != StopAgentContainer || rec.Phase != PhaseAgentStart || agent.calls != 0 {
		t.Fatalf("record = %s/%s at %s, %d agent runs; want an infra failure at agent-start with no agent run", rec.Outcome, rec.StopReason, rec.Phase, agent.calls)
	}
}

func TestEveryRegisteredHarnessCanRunInAContainer(t *testing.T) {
	c := AgentContainer{Image: pinnedChecksImage(t), GitCommonDir: "/repo/.git"}
	for _, r := range harnessRegistry {
		h := r.build(func(name string) string {
			if name == "RUNNER_TEMP" {
				return t.TempDir()
			}
			return ""
		})
		ch, ok := h.(containerHarness)
		if !ok {
			t.Errorf("harness %s cannot run in a container, so an agent image would not contain it", r.name)
			continue
		}
		contained, _, err := WithAgentContainer([]Harness{h}, c)
		if err != nil {
			t.Fatal(err)
		}
		if got := reflect.ValueOf(contained[0]).FieldByName("Container"); got.Interface() != c {
			t.Errorf("WithAgentContainer left harness %s with container %v", r.name, got)
		}
		// The probe's check must be the binary the image provides for this
		// harness, over that harness's home (tools/agent/check-image.sh).
		check := ch.startUpCheck()
		if filepath.Base(check.Bin) != r.name || check.Home == "" || !slices.Equal(check.Args, []string{"--version"}) {
			t.Errorf("harness %s probes %q %v over home %q, want its own version command over that harness's home", r.name, check.Bin, check.Args, check.Home)
		}
	}
}

// hostOnlyHarness cannot run in a container.
type hostOnlyHarness struct{ Harness }

func (hostOnlyHarness) Name() string { return "host-only" }

func TestWithAgentContainerRefusesAHarnessThatCannotBeContained(t *testing.T) {
	if _, _, err := WithAgentContainer([]Harness{hostOnlyHarness{}}, AgentContainer{Image: pinnedChecksImage(t)}); err == nil {
		t.Fatal("WithAgentContainer let a harness run on the host under an agent image")
	}
}

func TestModelWorkflowPassesTheAgentImageVariableToTheBuild(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "model.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yml := string(raw)
	for want, count := range map[string]int{
		"AGENT_IMAGE: ${{ vars.WINGMAN_AGENT_IMAGE }}": 3,
		`runner" build `:      1,
		`runner" plan-stage `: 1,
		`-agent-image "$AGENT_IMAGE" -agent-egress "$AGENT_EGRESS"'`: 2,
		"AGENT_EGRESS: ${{ vars.WINGMAN_AGENT_EGRESS }}":             2,
		`if: vars.WINGMAN_AGENT_IMAGE != ''`:                         0,
		`docker pull "$AGENT_IMAGE"`:                                 1,
		"--filter name=" + egressProxyPrefix:                         1,
		"--filter name=" + egressNetworkPrefix:                       1,
		"./cmd/runner\n        env:\n          CGO_ENABLED: \"0\"":   1,
	} {
		if got := strings.Count(yml, want); got != count {
			t.Errorf("model.yml has %d of %q, want %d", got, want, count)
		}
	}
}

// liveAgentContainer is a container over the pinned checks image, whose shell
// stands in for an agent, with a worktree and home to write to.
func liveAgentContainer(t *testing.T) (c AgentContainer, dir, home string) {
	t.Helper()
	skipWithoutDocker(t)
	repo := initRepo(t)
	wt, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/x")
	if err != nil {
		t.Fatal(err)
	}
	return AgentContainer{Image: pinnedChecksImage(t), GitCommonDir: wt.CommonDir}, wt.Dir, t.TempDir()
}

func TestAgentContainerReachesNoHostFileOutsideItsMounts(t *testing.T) {
	c, dir, home := liveAgentContainer(t)
	hostOnly := t.TempDir()
	creds := filepath.Join(hostOnly, "creds.json")
	fileCommand := filepath.Join(hostOnly, "set_output")
	for _, f := range []string{creds, fileCommand} {
		if err := os.WriteFile(f, []byte("host\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `
cat "$1" 2>/dev/null && echo LEAK-read-creds
echo x >> "$2" 2>/dev/null && echo LEAK-wrote-file-command
echo x > /etc/probe 2>/dev/null && echo LEAK-wrote-rootfs
echo x > "$3/probe" 2>/dev/null && echo LEAK-wrote-git-dir
echo x > "$4/ok" && echo x > "$5/ok" && echo mounts-writable
`
	var out bytes.Buffer
	err := c.Exec(context.Background(), dir, home,
		[]string{"sh", "-c", script, "probe", creds, fileCommand, c.GitCommonDir, dir, home}, &out, &out)
	if err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "LEAK") || !strings.Contains(out.String(), "mounts-writable") {
		t.Fatalf("probe output:\n%s", out.String())
	}
	if got, _ := os.ReadFile(fileCommand); string(got) != "host\n" {
		t.Errorf("the file-command file changed: %q", got)
	}
}

func TestAgentContainerLeavesNoContainerBehindWhenCancelled(t *testing.T) {
	c, dir, home := liveAgentContainer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Exec(ctx, dir, home, []string{"sleep", "300"}, io.Discard, io.Discard)

	deadline := time.Now().Add(15 * time.Second)
	for {
		out, err := exec.Command("docker", "ps", "-aq", "--filter", "name=wingman-agent-").Output()
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(out)) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("containers left after cancel: %s", out)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// classifiedAgent is an agent that classifies its own failures through its harness, as a Router does.
type classifiedAgent struct {
	Agent
	h Harness
}

func (a classifiedAgent) Classify(stderrTail string, exitErr error) (Outcome, StopReason) {
	return a.h.Classify(stderrTail, exitErr)
}

// containerFailureAgent is an omp agent over a fake docker that exits code.
func containerFailureAgent(t *testing.T, code string) classifiedAgent {
	t.Helper()
	fakeDocker(t)
	t.Setenv("DOCKER_FAKE_FAIL", "run")
	t.Setenv("DOCKER_FAKE_CODE", code)
	h := OmpHarness{Bin: "omp", Key: conformanceKey, Home: t.TempDir(), Container: testAgentContainer(t)}
	return classifiedAgent{Agent: h.Agent(ProfileBuild, ompTestModel), h: h}
}

func TestContainerAgentClassifiesDockerFailingToRunItAsInfrastructure(t *testing.T) {
	tests := []struct {
		code        string
		wantOutcome Outcome
		wantReason  StopReason
	}{
		{"125", OutcomeInfraFailure, StopAgentContainer},
		{"126", OutcomeInfraFailure, StopAgentContainer},
		{"127", OutcomeInfraFailure, StopAgentContainer},
		{"1", OutcomeAgentFailed, StopAgentExit},
		{"137", OutcomeAgentFailed, StopAgentExit},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			agent := containerFailureAgent(t, tt.code)
			err := agent.Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard)
			if err == nil {
				t.Fatal("Run succeeded")
			}
			if outcome, reason := agent.Classify("", err); outcome != tt.wantOutcome || reason != tt.wantReason {
				t.Errorf("exit %s classified %s/%s, want %s/%s", tt.code, outcome, reason, tt.wantOutcome, tt.wantReason)
			}
		})
	}
}

func TestOpencodeContainerAgentClassifiesDockerFailingToRunItAsInfrastructure(t *testing.T) {
	fakeDocker(t)
	t.Setenv("DOCKER_FAKE_FAIL", "run")
	t.Setenv("DOCKER_FAKE_CODE", "127")
	h := OpencodeHarness{Bin: "opencode", Key: conformanceKey, Home: t.TempDir(), Container: testAgentContainer(t)}
	err := h.Agent(ProfileBuild, ompTestModel).Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Run succeeded")
	}
	if outcome, reason := h.Classify("", err); outcome != OutcomeInfraFailure || reason != StopAgentContainer {
		t.Errorf("classified %s/%s, want infra-failure/agent-container", outcome, reason)
	}
}

func TestHostAgentKeepsAnExitOf127AsAnAgentFailure(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "omp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 127\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := OmpHarness{Bin: bin, Key: conformanceKey, Home: t.TempDir()}
	err := h.Agent(ProfileBuild, ompTestModel).Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Run succeeded")
	}
	if outcome, reason := h.Classify("", err); outcome != OutcomeAgentFailed || reason != StopAgentExit {
		t.Errorf("host exit 127 classified %s/%s, want agent-failed/agent-exit", outcome, reason)
	}
}

func TestBuildEndsAsAnInfrastructureFailureWhenDockerCannotRunTheAgent(t *testing.T) {
	tests := []struct {
		name      string
		size      string
		wantPhase Phase
	}{
		{"build agent", "S", PhaseBuild},
		{"plan agent", "M", PhasePlan},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := containerFailureAgent(t, "125")
			objects := validObjects()
			objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
			deps, _, reported := testDeps(objects, nil)
			deps.Agent, deps.PlanAgent = agent, agent
			c := testConfig(t, initRepo(t))
			c.Model, c.PlanModels = ompTestModel, []string{ompTestModel}
			c.Ticket.Size = tt.size

			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build succeeded with docker unable to run the agent")
			}
			rec := reported.last(t)
			if rec.Outcome != OutcomeInfraFailure || rec.StopReason != StopAgentContainer || rec.Phase != tt.wantPhase {
				t.Fatalf("record = %s/%s at %s, want infra-failure/agent-container at %s", rec.Outcome, rec.StopReason, rec.Phase, tt.wantPhase)
			}
		})
	}
}

func planStageDeps(t *testing.T, agent *fakeAgent) (BuildDeps, BuildConfig) {
	t.Helper()
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	deps, _, _ := testDeps(objects, nil)
	deps.PlanAgent = agent
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"
	return deps, c
}

func TestPlanStageRunsAContainedAgentInAProbedWorktreeOfItsOwn(t *testing.T) {
	agent := &fakeAgent{events: planEvent("```plan-files\nversion.go\n```")}
	deps, c := planStageDeps(t, agent)
	var probed string
	deps.AgentProbe = func(_ context.Context, dir string) error { probed = dir; return nil }

	if _, err := PlanStage(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(c.TempDir, "wt")
	if probed != want || !slices.Equal(agent.dirs, []string{want}) {
		t.Fatalf("probed %q, agent ran in %v, want both in %s and not the checkout %s", probed, agent.dirs, want, c.Repo)
	}
}

func TestPlanStageRunsAHostAgentInTheCheckout(t *testing.T) {
	agent := &fakeAgent{events: planEvent("```plan-files\nversion.go\n```")}
	deps, c := planStageDeps(t, agent)

	if _, err := PlanStage(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(agent.dirs, []string{c.Repo}) {
		t.Fatalf("agent ran in %v, want the checkout %s", agent.dirs, c.Repo)
	}
}

func TestPlanStageStopsBeforeAnyAgentRunsWhenTheAgentContainerWillNotStart(t *testing.T) {
	agent := &fakeAgent{}
	deps, c := planStageDeps(t, agent)
	deps.AgentProbe = func(context.Context, string) error { return io.ErrUnexpectedEOF }

	_, err := PlanStage(context.Background(), deps, c)
	var stop *StopError
	if !errors.As(err, &stop) || stop.Outcome != OutcomeInfraFailure || stop.Reason != StopAgentContainer || agent.calls != 0 {
		t.Fatalf("PlanStage = %v with %d agent runs, want an infra-failure/agent-container stop and no run", err, agent.calls)
	}
}
