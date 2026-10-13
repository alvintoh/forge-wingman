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

func TestAgentContainerRunArgsAreUnchangedWithEgressOff(t *testing.T) {
	c := AgentContainer{Image: "img@sha256:" + strings.Repeat("a", 64), GitCommonDir: "/repo/.git"}
	s := agentSpec{Bin: "/host/bin/omp", Args: []string{"-p"}, Dir: "/wt", Home: "/home/agent", Env: []string{"HOME=/home/agent"}}

	want := []string{
		"run", "--rm", "--name", "n", "--init", "--interactive",
		"--read-only", "--tmpfs", "/tmp:exec", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--mount", "type=bind,src=/wt,dst=/wt",
		"--mount", "type=bind,src=/home/agent,dst=/home/agent",
		"--mount", "type=bind,src=/repo/.git,dst=/repo/.git,readonly",
		"--workdir", "/wt",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--env", "TMPDIR=/tmp", "--env", "HOME=/home/agent", "--env", "KEY",
		c.Image, "omp", "-p",
	}
	if got := c.runArgs("n", "KEY", s); !slices.Equal(got, want) {
		t.Fatalf("runArgs =\n%v\nwant\n%v", got, want)
	}
}

func TestAgentContainerRunArgsJoinOnlyTheInternalNetworkThroughTheProxy(t *testing.T) {
	c := AgentContainer{Image: "img@sha256:" + strings.Repeat("a", 64), GitCommonDir: "/repo/.git",
		Network: "wingman-egress-net-1", ProxyURL: "http://wingman-egress-proxy-1:3128"}
	args := c.runArgs("n", "", agentSpec{Bin: "omp", Dir: "/wt"})

	if got := flagValues(args, "--network"); !slices.Equal(got, []string{"wingman-egress-net-1"}) {
		t.Errorf("--network %v, want the internal network alone", got)
	}
	env := flagValues(args, "--env")
	for _, kv := range []string{"HTTPS_PROXY=http://wingman-egress-proxy-1:3128", "HTTP_PROXY=http://wingman-egress-proxy-1:3128", "NO_PROXY=127.0.0.1,localhost",
		"https_proxy=http://wingman-egress-proxy-1:3128", "http_proxy=http://wingman-egress-proxy-1:3128", "no_proxy=127.0.0.1,localhost"} {
		if !slices.Contains(env, kv) {
			t.Errorf("env %v lacks %s", env, kv)
		}
	}
}

func TestEgressHostsComeFromTheModelsPlans(t *testing.T) {
	got, err := EgressHosts([]string{"gemini/gemini-3.8-flash", "command-code/deepseek/deepseek-v4.1-flash", "command-code/x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api.commandcode.ai", "generativelanguage.googleapis.com"}; !slices.Equal(got, want) {
		t.Fatalf("hosts %v, want %v", got, want)
	}
	if _, err := EgressHosts([]string{"nowhere/model"}); err == nil {
		t.Fatal("a model of an unknown plan produced hosts")
	}
}

func TestStartEgressBuildsTheNetworkAndProxyAndTearsThemDownAfterCancel(t *testing.T) {
	logFile := fakeDocker(t)
	c := testAgentContainer(t)
	bin := t.TempDir() + "/runner"

	joined, teardown, err := StartEgress(context.Background(), c, bin, []string{"api.commandcode.ai"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(joined.Network, egressNetworkPrefix) || !strings.HasPrefix(joined.ProxyURL, "http://"+egressProxyPrefix) {
		t.Fatalf("joined container = %+v, want prefixed network and proxy", joined)
	}
	calls := dockerCalls(t, logFile)
	if calls[0][0] != "network" || !slices.Contains(calls[0], "--internal") || !slices.Contains(calls[0], joined.Network) {
		t.Errorf("first call %v, want an internal network create", calls[0])
	}
	run := calls[1]
	if !slices.Contains(run, "--detach") || !slices.Contains(run, "--read-only") || !slices.Contains(run, "api.commandcode.ai") {
		t.Errorf("proxy run %v lacks detach, read-only or the allowlist", run)
	}
	for _, f := range []string{"--cap-drop", "no-new-privileges", "--init"} {
		if !slices.Contains(run, f) {
			t.Errorf("proxy run %v lacks %s", run, f)
		}
	}
	if got := flagValues(run, "--log-opt"); !slices.Equal(got, []string{"max-size=1m", "max-file=1"}) {
		t.Errorf("proxy log options %v, want a 1m single-file cap", got)
	}
	if slices.Contains(run, "--env") {
		t.Errorf("proxy run %v passes the container an environment", run)
	}
	if mounts := flagValues(run, "--mount"); len(mounts) != 1 || !strings.HasSuffix(mounts[0], ",readonly") {
		t.Errorf("proxy mounts %v, want the runner binary alone, read-only", mounts)
	}
	if slices.Contains(run, "--network") {
		t.Errorf("proxy run %v joins a network before the internal one is connected", run)
	}
	if got := calls[2]; got[0] != "network" || got[1] != "connect" || got[2] != joined.Network {
		t.Errorf("third call %v, want the proxy connected to the internal network", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := teardown(ctx); err != nil {
		t.Fatalf("teardown after cancel: %v", err)
	}
	calls = dockerCalls(t, logFile)
	if got := calls[len(calls)-3]; !slices.Equal(got[:3], []string{"logs", "--tail", "200"}) {
		t.Errorf("log read %v, want a bounded tail", got)
	}
	last := calls[len(calls)-2:]
	if last[0][0] != "rm" || last[0][1] != "--force" || last[1][0] != "network" || last[1][1] != "rm" || last[1][2] != joined.Network {
		t.Errorf("teardown ran %v, want proxy removal then network removal", last)
	}
}

func TestStartEgressRemovesWhatItCreatedWhenTheProxyFailsToStart(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_FAIL", "--detach")
	c := testAgentContainer(t)

	if _, _, err := StartEgress(context.Background(), c, t.TempDir()+"/runner", []string{"api.commandcode.ai"}, io.Discard); err == nil {
		t.Fatal("StartEgress succeeded with a proxy that failed to start")
	}
	calls := dockerCalls(t, logFile)
	if got := calls[len(calls)-1]; got[0] != "network" || got[1] != "rm" {
		t.Fatalf("last call %v, want the network removed", got)
	}
}

func TestStartEgressNeedsHostsAndAMountablePath(t *testing.T) {
	fakeDocker(t)
	c := testAgentContainer(t)
	if _, _, err := StartEgress(context.Background(), c, "/bin/runner", nil, io.Discard); err == nil {
		t.Error("no hosts accepted")
	}
	if _, _, err := StartEgress(context.Background(), c, "relative,runner", []string{"h"}, io.Discard); err == nil {
		t.Error("an unmountable binary path accepted")
	}
}

func TestStartEgressRemovesWhatItCreatedWhenTheProxyNeverAcceptsConnections(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_FAIL", "exec")
	old := egressReadyTimeout
	egressReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { egressReadyTimeout = old })

	_, _, err := StartEgress(context.Background(), testAgentContainer(t), t.TempDir()+"/runner", []string{"api.commandcode.ai"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("StartEgress = %v, want a not-ready error", err)
	}
	calls := dockerCalls(t, logFile)
	if got := calls[len(calls)-1]; got[0] != "network" || got[1] != "rm" {
		t.Fatalf("last call %v, want the network removed", got)
	}
}

func TestStartEgressTeardownRelaysOnlyTheProxysRefusalsAndFailures(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = logs ]; then
	printf '%s\n' '{"msg":"egressProxyListening"}' '{"msg":"egressRefused","reason":"host-not-allowed","host":"evil.example.com"}' '{"msg":"egressFailed","reason":"dial-failed","host":"api.commandcode.ai"}'
fi
exit 0
`
	if err := os.WriteFile(bin+"/docker", []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var logs strings.Builder
	_, teardown, err := StartEgress(context.Background(), testAgentContainer(t), t.TempDir()+"/runner", []string{"api.commandcode.ai"}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if err := teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := `{"msg":"egressRefused","reason":"host-not-allowed","host":"evil.example.com"}` + "\n" +
		`{"msg":"egressFailed","reason":"dial-failed","host":"api.commandcode.ai"}` + "\n"
	if logs.String() != want {
		t.Fatalf("relayed %q, want only the refusal and the failure %q", logs.String(), want)
	}
}

func TestStartEgressTeardownCapsWhatItRelays(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = logs ]; then
	i=0
	while [ $i -lt 200 ]; do printf '{"msg":"egressRefused","pad":"%01000d"}\n' 0; i=$((i+1)); done
fi
exit 0
`
	if err := os.WriteFile(bin+"/docker", []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var logs strings.Builder
	_, teardown, err := StartEgress(context.Background(), testAgentContainer(t), t.TempDir()+"/runner", []string{"api.commandcode.ai"}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if err := teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := logs.Len(); n == 0 || n > egressRelayBytes {
		t.Fatalf("relayed %d bytes, want some but at most %d", n, egressRelayBytes)
	}
}
