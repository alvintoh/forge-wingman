package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const (
	// egressNetworkPrefix and egressProxyPrefix begin the name of every network and
	// proxy container a run creates, so a sweep can remove what a killed job left.
	egressNetworkPrefix = "wingman-egress-net-"
	egressProxyPrefix   = "wingman-egress-proxy-"

	egressProxyPort = "3128"
	// egressRelayLines and egressRelayBytes bound what a teardown reads from the
	// proxy's log and writes on.
	egressRelayLines = "200"
	egressRelayHead  = 20
	egressRelayBytes = 64 << 10
	egressProxyBin   = "/usr/local/bin/wingman-runner"
)

// egressReadyTimeout bounds the wait for the proxy to accept connections.
var egressReadyTimeout = 15 * time.Second

// EgressHosts is the API hosts of the plans models name, sorted and without
// repeats, read from each plan's base URL. A model whose plan has no base URL
// is an error: the agent could not reach it through the proxy.
func EgressHosts(models []string) ([]string, error) {
	var hosts []string
	for _, m := range models {
		plan, _, _ := strings.Cut(m, "/")
		raw := providers.BaseURL(plan)
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("model %q: plan %q has no base URL (%q)", m, plan, raw)
		}
		hosts = append(hosts, normalizeHost(u.Hostname()))
	}
	slices.Sort(hosts)
	return slices.Compact(hosts), nil
}

// StartEgress confines agents to hosts: it creates an internal network, starts a
// proxy on it and on the default network from c's image with the runner binary
// proxyBin mounted, and returns c joined to the internal network only, with the
// proxy in its environment, and a teardown that removes both.
//
// The teardown runs on a context of its own, since the run's may have ended, and
// first writes the proxy's refusal and failure log lines to logs.
func StartEgress(ctx context.Context, c AgentContainer, proxyBin string, hosts []string, logs io.Writer) (AgentContainer, func(context.Context) error, error) {
	if len(hosts) == 0 {
		return AgentContainer{}, nil, errors.New("no hosts to allow through the egress proxy")
	}
	if err := checkMountPath("egress proxy binary", proxyBin); err != nil {
		return AgentContainer{}, nil, err
	}
	network, err := containerName(egressNetworkPrefix)
	if err != nil {
		return AgentContainer{}, nil, err
	}
	proxy, err := containerName(egressProxyPrefix)
	if err != nil {
		return AgentContainer{}, nil, err
	}
	teardown := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerKillTimeout)
		defer cancel()
		relayProxyLog(ctx, proxy, logs)
		return errors.Join(docker(ctx, "rm", "--force", proxy), docker(ctx, "network", "rm", network))
	}
	if err := startProxy(ctx, c, network, proxy, proxyBin, hosts); err != nil {
		return AgentContainer{}, nil, errors.Join(err, teardown(ctx))
	}
	c.Network = network
	c.ProxyURL = "http://" + proxy + ":" + egressProxyPort
	return c, teardown, nil
}

func startProxy(ctx context.Context, c AgentContainer, network, proxy, proxyBin string, hosts []string) error {
	if err := docker(ctx, "network", "create", "--internal", network); err != nil {
		return err
	}
	args := []string{
		"run", "--detach", "--name", proxy, "--init",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--log-opt", "max-size=1m", "--log-opt", "max-file=1", "--log-opt", "compress=false",
		"--mount", bindMount(proxyBin, egressProxyBin, true),
	}
	if uid, gid := os.Getuid(), os.Getgid(); uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid))
	}
	args = append(args, c.Image, egressProxyBin, "egress-proxy", "-listen", ":"+egressProxyPort, "-allow", strings.Join(hosts, ","))
	if err := docker(ctx, args...); err != nil {
		return err
	}
	if err := docker(ctx, "network", "connect", network, proxy); err != nil {
		return err
	}
	return waitProxyReady(ctx, proxy)
}

// waitProxyReady polls the proxy's own readiness check until it accepts connections.
func waitProxyReady(ctx context.Context, proxy string) error {
	ctx, cancel := context.WithTimeout(ctx, egressReadyTimeout)
	defer cancel()
	for {
		err := docker(ctx, "exec", proxy, egressProxyBin, "egress-proxy", "-ready", "127.0.0.1:"+egressProxyPort)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("egress proxy not ready: %w", errors.Join(ctx.Err(), err))
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// isProxyEvent reports whether a log line is a refusal or a failure.
func isProxyEvent(line string) bool {
	return strings.Contains(line, `"msg":"`+egressRefusedEvent+`"`) || strings.Contains(line, `"msg":"`+egressFailedEvent+`"`)
}

// relayProxyLog stops the proxy, so it logs its summary, then writes to w the
// first and the last of its refusal and failure lines and then the summary, so a
// call it blocked shows in the runner's own log even after a flood has rolled
// the early lines out. A run the proxy let through writes nothing but the
// summary. It is best effort: the teardown must go on without the log, and the
// container is left for the teardown to remove, so its log stays readable.
func relayProxyLog(ctx context.Context, proxy string, w io.Writer) {
	_ = docker(ctx, "stop", "--time", "5", proxy)
	out, _ := dockerOutput(ctx, "logs", "--tail", egressRelayLines, proxy)
	var tail []string
	var summary string
	inTail := map[string]bool{}
	for line := range strings.Lines(out) {
		switch {
		case strings.Contains(line, `"msg":"`+egressSummaryEvent+`"`):
			summary = line
		case isProxyEvent(line):
			tail = append(tail, line)
			inTail[line] = true
		}
	}
	written := 0
	emit := func(line string) bool {
		if written += len(line); written > egressRelayBytes {
			return false
		}
		_, _ = io.WriteString(w, line)
		return true
	}
	for _, line := range firstProxyEvents(ctx, proxy, egressRelayHead) {
		if !inTail[line] && !emit(line) {
			break
		}
	}
	for _, line := range tail {
		if !emit(line) {
			break
		}
	}
	_, _ = io.WriteString(w, summary)
}

// firstProxyEvents is the first n refusal and failure lines of the proxy's log,
// read as a stream that is dropped once it has them.
func firstProxyEvents(ctx context.Context, proxy string, n int) []string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "logs", proxy)
	cmd.Env = filterEnv(os.Environ(), dockerEnvNames)
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return nil
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	var lines []string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for len(lines) < n && sc.Scan() {
		if isProxyEvent(sc.Text()) {
			lines = append(lines, sc.Text()+"\n")
		}
	}
	return lines
}

// docker runs one docker CLI command to completion.
func docker(ctx context.Context, args ...string) error {
	_, err := dockerOutput(ctx, args...)
	return err
}

// dockerOutput runs one docker CLI command to completion and returns what it printed.
func dockerOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = filterEnv(os.Environ(), dockerEnvNames)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w: %s", args[0], err, truncate(strings.TrimSpace(string(out)), logErrorLimit))
	}
	return string(out), nil
}
