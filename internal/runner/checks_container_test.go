package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// pinnedChecksImage is the checks image model.yml pulls and passes to the runner.
func pinnedChecksImage(t *testing.T) string {
	t.Helper()
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "model.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s+CHECKS_IMAGE: (\S+)$`).FindSubmatch(yml)
	if m == nil {
		t.Fatal("model.yml sets no CHECKS_IMAGE")
	}
	return string(m[1])
}

// fakeDocker puts a docker script first on PATH that logs each invocation's
// arguments, one invocation per line, to the returned file. DOCKER_FAKE_FAIL
// names an argument whose run prints "boom" and exits DOCKER_FAKE_CODE (1 by
// default); DOCKER_FAKE_LIST makes gofmt list a file; DOCKER_FAKE_HANG makes
// every run hang. Each invocation's environment is appended to logFile+".env".
func fakeDocker(t *testing.T) (logFile string) {
	t.Helper()
	bin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "docker.log")
	script := `#!/bin/sh
{ printf '%s\037' "$@"; printf '\n'; } >> "` + logFile + `"
env >> "` + logFile + `.env"
[ "$1" = kill ] && exit 0
for a in "$@"; do
	if [ -n "$DOCKER_FAKE_FAIL" ] && [ "$a" = "$DOCKER_FAKE_FAIL" ]; then echo boom; exit "${DOCKER_FAKE_CODE:-1}"; fi
	if [ -n "$DOCKER_FAKE_LIST" ] && [ "$a" = gofmt ]; then echo x.go; exit 0; fi
done
[ -n "$DOCKER_FAKE_HANG" ] && exec sleep 30
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

// dockerCalls reads fakeDocker's log back, one argument list per invocation.
func dockerCalls(t *testing.T, logFile string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for line := range strings.Lines(string(raw)) {
		calls = append(calls, strings.Split(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\x1f"), "\x1f"))
	}
	return calls
}

// testContainerChecks is a valid configuration whose paths exist.
func testContainerChecks(t *testing.T) ContainerChecks {
	t.Helper()
	linter := filepath.Join(t.TempDir(), "golangci-lint")
	if err := os.WriteFile(linter, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return ContainerChecks{Image: pinnedChecksImage(t), ModCache: t.TempDir(), Linter: linter, GitCommonDir: t.TempDir(),
		Bun: fakeBun(t, filepath.Join(t.TempDir(), "bun")), BunCache: t.TempDir()}
}

// fakeBun writes a bun script at path that logs each invocation — the path it
// was run as, the working directory it ran in, then its arguments, one
// \x1f-separated line per invocation — to bunLog(path), and that prints "boom",
// exits 1 and logs nothing while bunFails has armed bunFail(path).
//
// A marker file rather than an environment variable, as fakeDocker uses: a gate
// subprocess runs under checkCmdEnv's allow-list, which admits no variable a
// test invents, and the docker CLI is allowed DOCKER_* alone.
func fakeBun(t *testing.T, path string) string {
	t.Helper()
	script := `#!/bin/sh
want=$(cat "` + bunFail(path) + `" 2>/dev/null)
for a in "$@"; do
	if [ -n "$want" ] && [ "$a" = "$want" ]; then echo boom; exit 1; fi
done
{ printf '%s\037' "$0" "$(pwd)" "$@"; printf '\n'; } >> "` + bunLog(path) + `"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// bunLog is where the bun script at path logs.
func bunLog(path string) string { return path + ".log" }

// bunFail is the marker the bun script at path reads its failing argument from.
func bunFail(path string) string { return path + ".fail" }

// bunFails arms the bun script at path to fail when one of its arguments is arg.
func bunFails(t *testing.T, path, arg string) {
	t.Helper()
	if err := os.WriteFile(bunFail(path), []byte(arg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// bunCalls reads the log of the bun script at path back, one invocation's path,
// working directory and arguments per entry.
func bunCalls(t *testing.T, path string) [][]string {
	t.Helper()
	return dockerCalls(t, bunLog(path))
}

// webTouched is a run's changed paths for a diff that touches the front end.
var webTouched = []string{"internal/runner/checks.go", webDir + "/src/app.tsx"}

// initWebModule is a module whose web directory exists, as a diff that touches
// web implies, so the bun install's working directory resolves. The path comes
// back fully resolved, since a shell run in it reports the resolved path
// (macOS's temporary directory sits behind a symlink).
func initWebModule(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(initModule(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, webDir), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// flagValues is every value args gives flag.
func flagValues(args []string, flag string) []string {
	var vals []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			vals = append(vals, args[i+1])
		}
	}
	return vals
}

func TestContainerChecksRunEachGateInAContainerWithNoNetworkAndOnlyItsMounts(t *testing.T) {
	logFile := fakeDocker(t)
	c := testContainerChecks(t)
	dir := initModule(t)

	gate, output, err := c.Run(context.Background(), dir, nil)
	if err != nil || gate != "" {
		t.Fatalf("Run = %q, %q, %v, want a clean pass", gate, output, err)
	}
	calls := dockerCalls(t, logFile)
	var commands []string
	for _, args := range calls {
		if args[0] != "run" || !slices.Contains(args, "--rm") || !slices.Contains(args, "--init") {
			t.Fatalf("docker %v, want docker run --rm --init", args)
		}
		if got := flagValues(args, "--network"); !slices.Equal(got, []string{"none"}) {
			t.Errorf("--network %v, want none", got)
		}
		if got := flagValues(args, "--tmpfs"); !slices.Equal(got, []string{"/tmp:exec"}) {
			t.Errorf("--tmpfs %v, want /tmp:exec alone", got)
		}
		for _, f := range []string{"-v", "--volume", "--env-file", "--privileged", "--net", "-e"} {
			if slices.Contains(args, f) {
				t.Errorf("docker run carries %s: %v", f, args)
			}
		}
		at := slices.Index(args, c.Image)
		if at < 0 {
			t.Fatalf("docker run %v does not name the configured image", args)
		}
		commands = append(commands, strings.Join(args[at+1:], " "))

		mounts := flagValues(args, "--mount")
		var cache string
		for _, m := range mounts {
			if src, ok := strings.CutSuffix(m, ",dst=/cache"); ok {
				cache = strings.TrimPrefix(src, "type=bind,src=")
			}
		}
		if !strings.HasPrefix(cache, filepath.Join(os.TempDir(), "wingman-checks-")) {
			t.Errorf("build cache at %q, want a throwaway directory", cache)
		}
		wantMounts := []string{
			"type=bind,src=" + dir + ",dst=/work",
			"type=bind,src=" + c.ModCache + ",dst=/gomodcache,readonly",
			"type=bind,src=" + cache + ",dst=/cache",
			"type=bind,src=" + c.Linter + ",dst=/usr/local/bin/golangci-lint,readonly",
			"type=bind,src=" + c.GitCommonDir + ",dst=" + c.GitCommonDir + ",readonly",
		}
		if !slices.Equal(mounts, wantMounts) {
			t.Errorf("mounts = %v, want only %v", mounts, wantMounts)
		}
		if _, err := os.Stat(cache); !os.IsNotExist(err) {
			t.Errorf("build cache %s outlived the round: %v", cache, err)
		}
		wantEnv := []string{
			"GOMODCACHE=/gomodcache", "GOCACHE=/cache/go-build", "GOLANGCI_LINT_CACHE=/cache/lint",
			"TMPDIR=/tmp", "HOME=/cache/home", "GOPROXY=off", "GOFLAGS=-mod=readonly -buildvcs=false",
			"GOTOOLCHAIN=local",
		}
		if got := flagValues(args, "--env"); !slices.Equal(got, wantEnv) {
			t.Errorf("env = %v, want %v", got, wantEnv)
		}
	}
	want := []string{"gofmt -l .", "go vet ./...", "golangci-lint run", "go test -race -shuffle=on -cover ./..."}
	if !slices.Equal(commands, want) {
		t.Fatalf("gates ran %q, want %q", commands, want)
	}
}

func TestContainerChecksReportAFailingGateWithItsOutput(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"gofmt lists a file", map[string]string{"DOCKER_FAKE_LIST": "1"}, checkGofmt},
		{"vet exits non-zero", map[string]string{"DOCKER_FAKE_FAIL": "vet"}, checkVet},
		{"test exits non-zero", map[string]string{"DOCKER_FAKE_FAIL": "-race"}, checkTest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeDocker(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			gate, output, err := testContainerChecks(t).Run(context.Background(), initModule(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			if gate != tt.want || output == "" {
				t.Fatalf("gate = %q, output = %q, want %q with its output", gate, output, tt.want)
			}
		})
	}
}

// TestContainerChecksRunTheWebStepsInFreshContainersOnlyWhenTheDiffTouchesWeb
// is the run's own verdict covering ci.yml's web job: a diff that does not
// touch web runs the Go gates alone and never the host's bun, and one that does
// runs the web job's steps after them, each in a fresh container of the checks
// image.
func TestContainerChecksRunTheWebStepsInFreshContainersOnlyWhenTheDiffTouchesWeb(t *testing.T) {
	t.Run("a diff that does not touch web", func(t *testing.T) {
		logFile := fakeDocker(t)
		c := testContainerChecks(t)

		gate, output, err := c.Run(context.Background(), initWebModule(t), []string{"internal/runner/checks.go"})
		if err != nil || gate != "" {
			t.Fatalf("Run = %q, %q, %v, want a clean pass", gate, output, err)
		}
		want := []string{"gofmt -l .", "go vet ./...", "golangci-lint run", "go test -race -shuffle=on -cover ./..."}
		if got := imageCommands(t, dockerCalls(t, logFile), c.Image); !slices.Equal(got, want) {
			t.Fatalf("a diff that does not touch web ran %q, want the Go gates alone %q", got, want)
		}
		if _, err := os.Stat(bunLog(c.Bun)); !os.IsNotExist(err) {
			t.Errorf("the host's bun ran for a diff that does not touch web: %v", err)
		}
	})

	t.Run("a diff that touches web", func(t *testing.T) {
		logFile := fakeDocker(t)
		c := testContainerChecks(t)
		dir := initWebModule(t)

		gate, output, err := c.Run(context.Background(), dir, webTouched)
		if err != nil || gate != "" {
			t.Fatalf("Run = %q, %q, %v, want a clean pass", gate, output, err)
		}
		calls := dockerCalls(t, logFile)
		if len(calls) != 8 {
			t.Fatalf("docker ran %d containers, want the four Go gates then the four web steps: %v", len(calls), calls)
		}
		var commands []string
		for _, args := range calls[4:] {
			if args[0] != "run" || !slices.Contains(args, "--rm") || !slices.Contains(args, "--init") {
				t.Fatalf("docker %v, want docker run --rm --init", args)
			}
			if got := flagValues(args, "--network"); !slices.Equal(got, []string{"none"}) {
				t.Errorf("--network %v, want none", got)
			}
			wantMounts := []string{
				"type=bind,src=" + dir + ",dst=/work",
				"type=bind,src=" + c.Bun + ",dst=" + containerBun + ",readonly",
				"type=bind,src=" + c.BunCache + ",dst=" + containerBunCache,
			}
			if got := flagValues(args, "--mount"); !slices.Equal(got, wantMounts) {
				t.Errorf("mounts = %v, want only %v", got, wantMounts)
			}
			if got := flagValues(args, "--workdir"); !slices.Equal(got, []string{containerWebDir}) {
				t.Errorf("--workdir %v, want %s", got, containerWebDir)
			}
			wantEnv := []string{"TMPDIR=/tmp", "HOME=/tmp", "BUN_INSTALL_CACHE_DIR=" + containerBunCache}
			if got := flagValues(args, "--env"); !slices.Equal(got, wantEnv) {
				t.Errorf("env = %v, want %v", got, wantEnv)
			}
			at := slices.Index(args, c.Image)
			if at < 0 {
				t.Fatalf("docker run %v does not name the configured image", args)
			}
			commands = append(commands, strings.Join(args[at+1:], " "))
		}
		want := []string{
			containerBun + " install --frozen-lockfile --offline",
			containerBun + " run lint",
			containerBun + " run fmt:check",
			containerBun + " run build",
		}
		if !slices.Equal(commands, want) {
			t.Fatalf("the web steps ran %q, want %q", commands, want)
		}

		// The host filled the cache first, with an install that runs no
		// repository code, where the web job's own install runs.
		warm := bunCalls(t, c.Bun)
		if len(warm) != 1 {
			t.Fatalf("the host's bun ran %v, want one install", warm)
		}
		if got := warm[0][0]; got != c.Bun {
			t.Errorf("the bun install ran as %q, want the binary it mounts %q", got, c.Bun)
		}
		if got := warm[0][1]; got != filepath.Join(dir, webDir) {
			t.Errorf("the bun install ran in %q, want the web directory", got)
		}
		wantWarm := "install --frozen-lockfile --ignore-scripts --cache-dir " + c.BunCache
		if got := strings.Join(warm[0][2:], " "); got != wantWarm {
			t.Errorf("the host's bun ran %q, want %q", got, wantWarm)
		}
	})
}

// TestContainerChecksNameTheFailingWebStep holds the cases the check gate must
// tell apart: each of the steps ci.yml's web job can fail at is named as
// itself, a clean web change passes with the steps run to the end, and a diff
// that does not touch web is left to the Go gates.
func TestContainerChecksNameTheFailingWebStep(t *testing.T) {
	tests := []struct {
		name    string
		changed []string
		failOn  string
		want    string
	}{
		{"an install failure", webTouched, "--frozen-lockfile", checkWebInstall},
		{"a lint failure", webTouched, "lint", checkWebLint},
		{"a fmt:check failure", webTouched, "fmt:check", checkWebFmtCheck},
		{"a build failure", webTouched, "build", checkWebBuild},
		{"a pass", webTouched, "", ""},
		{"a diff with no web change", []string{"internal/runner/checks.go"}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := fakeDocker(t)
			if tt.failOn != "" {
				t.Setenv("DOCKER_FAKE_FAIL", tt.failOn)
			}
			gate, output, err := testContainerChecks(t).Run(context.Background(), initWebModule(t), tt.changed)
			if err != nil {
				t.Fatal(err)
			}
			if gate != tt.want {
				t.Fatalf("gate = %q, output = %q, want %q", gate, output, tt.want)
			}
			if tt.want != "" && output == "" {
				t.Fatal("no output captured for the failing step")
			}
			// A clean web change reaches the last of the steps, so this case
			// cannot pass with them skipped.
			if !touchesWeb(tt.changed) || tt.want != "" {
				return
			}
			calls := dockerCalls(t, logFile)
			if last := calls[len(calls)-1]; !slices.Contains(last, "build") {
				t.Fatalf("a clean web change ended at %v, want its build step", last)
			}
		})
	}
}

// imageCommands is every docker invocation's argv after the image it names, the
// command each container ran.
func imageCommands(t *testing.T, calls [][]string, image string) []string {
	t.Helper()
	var commands []string
	for _, args := range calls {
		at := slices.Index(args, image)
		if at < 0 {
			t.Fatalf("docker run %v does not name the configured image", args)
		}
		commands = append(commands, strings.Join(args[at+1:], " "))
	}
	return commands
}

func TestContainerChecksTreatDockerFailingToStartAGateAsAnError(t *testing.T) {
	fakeDocker(t)
	t.Setenv("DOCKER_FAKE_FAIL", "vet")
	t.Setenv("DOCKER_FAKE_CODE", "125")
	if gate, _, err := testContainerChecks(t).Run(context.Background(), initModule(t), nil); err == nil {
		t.Fatalf("Run reported gate %q, want an error for a container docker could not start", gate)
	}
}

func TestContainerChecksKillAGateThatTimesOutAndReportItFailed(t *testing.T) {
	logFile := fakeDocker(t)
	t.Setenv("DOCKER_FAKE_HANG", "1")
	c := testContainerChecks(t)
	c.GateTimeout = time.Second

	start := time.Now()
	gate, output, err := c.Run(context.Background(), initModule(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gate != checkGofmt || !strings.Contains(output, "stopped after 1s") {
		t.Fatalf("gate = %q, output = %q, want gofmt reported stopped", gate, output)
	}
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

func TestContainerChecksValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ContainerChecks)
		ok     bool
	}{
		{"a pinned image and absolute paths", func(*ContainerChecks) {}, true},
		{"an image pinned by tag alone", func(c *ContainerChecks) { c.Image = "golang:1.27.1" }, false},
		{"a short digest", func(c *ContainerChecks) { c.Image = "golang@sha256:162be5" }, false},
		{"a relative module cache", func(c *ContainerChecks) { c.ModCache = "modcache" }, false},
		{"a linter path a mount cannot carry", func(c *ContainerChecks) { c.Linter = "/bin/a,b" }, false},
		{"no bun", func(c *ContainerChecks) { c.Bun = "" }, false},
		{"no bun cache", func(c *ContainerChecks) { c.BunCache = "" }, false},
		{"no git common dir", func(c *ContainerChecks) { c.GitCommonDir = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ContainerChecks{Image: pinnedChecksImage(t), ModCache: "/modcache", Linter: "/bin/golangci-lint",
				Bun: "/bin/bun", BunCache: "/buncache", GitCommonDir: "/repo/.git"}
			tt.mutate(&c)
			if err := c.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate() = %v, want ok = %v", err, tt.ok)
			}
		})
	}
}

// skipWithoutDocker skips a test that needs a running Docker daemon.
func skipWithoutDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not running")
	}
}

// TestContainerChecksReachNoHostFileAndNoNetwork runs a real gate in the
// pinned image whose test tries to read a file on the host outside the mounts,
// as a credentials file would be, and to open a connection, then fails so its
// output comes back.
func TestContainerChecksReachNoHostFileAndNoNetwork(t *testing.T) {
	skipWithoutDocker(t)
	secret := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(secret, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := initModule(t)
	writeModuleFile(t, dir, "probe_test.go", `package x

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	if _, err := os.ReadFile(`+"`"+secret+"`"+`); err != nil {
		t.Log("read-blocked")
	}
	if _, err := net.DialTimeout("tcp", "1.1.1.1:443", 5*time.Second); err != nil {
		t.Log("dial-blocked")
	}
	t.Fatal("probe done")
}
`)
	gate, output, err := testContainerChecks(t).Run(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gate != checkTest || !strings.Contains(output, "probe done") {
		t.Fatalf("gate = %q, output = %q, want the probe test to have run", gate, output)
	}
	for _, want := range []string{"read-blocked", "dial-blocked"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
}

// TestContainerChecksRunGitInAWorktreeButCannotWriteItsGitDir runs a real gate
// in a git worktree whose test lists the tracked files and tries to write into
// the mounted git common directory, then fails so its output comes back.
func TestContainerChecksRunGitInAWorktreeButCannotWriteItsGitDir(t *testing.T) {
	skipWithoutDocker(t)
	repo := initRepo(t)
	writeModuleFile(t, repo, "go.mod", "module example.com/x\n\ngo 1.22\n")
	writeModuleFile(t, repo, "x.go", cleanGoSource)
	mustGit(t, repo, "add", "go.mod", "x.go")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "module")
	wt, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/x")
	if err != nil {
		t.Fatal(err)
	}
	writeModuleFile(t, wt.Dir, "git_test.go", `package x

import (
	"os"
	"os/exec"
	"testing"
)

func TestGit(t *testing.T) {
	out, err := exec.Command("git", "ls-files").CombinedOutput()
	t.Logf("ls-files err=%v\n%s", err, out)
	if err := os.WriteFile(`+"`"+filepath.Join(wt.CommonDir, "probe")+"`"+`, nil, 0o600); err != nil {
		t.Log("write-blocked")
	}
	t.Fatal("probe done")
}
`)
	c := testContainerChecks(t)
	c.GitCommonDir = wt.CommonDir
	gate, output, err := c.Run(context.Background(), wt.Dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gate != checkTest || !strings.Contains(output, "probe done") {
		t.Fatalf("gate = %q, output = %q, want the probe test to have run", gate, output)
	}
	for _, want := range []string{"ls-files err=<nil>", "x.go", "write-blocked"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestModelWorkflowRunsTheChecksInGoModsToolchainImage(t *testing.T) {
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	toolchain := regexp.MustCompile(`(?m)^toolchain go(\S+)$`).FindSubmatch(mod)
	if toolchain == nil {
		t.Fatal("go.mod names no toolchain")
	}
	image := pinnedChecksImage(t)
	if !strings.HasPrefix(image, "golang:"+string(toolchain[1])+"@sha256:") || !pinnedImage.MatchString(image) {
		t.Errorf("checks image %q, want golang:%s pinned by digest", image, toolchain[1])
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "model.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yml := string(raw)
	pull := strings.Index(yml, `run: docker pull "$CHECKS_IMAGE"`)
	build := strings.Index(yml, `-checks-image "$CHECKS_IMAGE"`)
	if pull < 0 || build < pull {
		t.Errorf("model.yml pulls the checks image at %d and passes it to the build at %d, want the pull first", pull, build)
	}
	if !strings.Contains(yml, "golangci-lint@v2.13.2\n        env:\n          CGO_ENABLED: \"0\"\n") {
		t.Error("model.yml does not build golangci-lint static, so the container cannot run it")
	}
}

// hangingBun is a bun that never exits, as a registry that never answers.
func hangingBun(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bun")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestContainerChecksStopAHungWebDependencyDownloadWithANamedError(t *testing.T) {
	fakeDocker(t)
	c := testContainerChecks(t)
	c.Bun = hangingBun(t)
	c.GateTimeout = time.Second

	start := time.Now()
	gate, _, err := c.Run(context.Background(), initWebModule(t), webTouched)
	if err == nil || !strings.Contains(err.Error(), "bun install: stopped after 1s") {
		t.Fatalf("Run = %q, %v, want a named error for the hung download", gate, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want the hung download killed", elapsed)
	}
}

func TestContainerChecksReportABunThatCannotStartAndLeaveAFailingInstallToItsGate(t *testing.T) {
	t.Run("bun cannot start", func(t *testing.T) {
		fakeDocker(t)
		c := testContainerChecks(t)
		c.Bun = filepath.Join(t.TempDir(), "missing-bun")

		if _, _, err := c.Run(context.Background(), initWebModule(t), webTouched); err == nil || !strings.Contains(err.Error(), "bun install:") {
			t.Fatalf("Run error = %v, want a bun install error", err)
		}
	})

	t.Run("bun exits non-zero", func(t *testing.T) {
		fakeDocker(t)
		c := testContainerChecks(t)
		bunFails(t, c.Bun, "--frozen-lockfile")

		if err := c.downloadWebModules(context.Background(), initWebModule(t)); err != nil {
			t.Fatalf("downloadWebModules = %v, want a failed install left to the web-install gate", err)
		}
	})
}
