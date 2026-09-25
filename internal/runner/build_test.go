package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const testSHA = "ee66687aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeObjects map[string][]byte

func (f fakeObjects) ReadObject(_ context.Context, name string) ([]byte, error) {
	b, ok := f[name]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return b, nil
}

func (f fakeObjects) CreateObject(_ context.Context, name string, r io.Reader) error {
	if _, ok := f[name]; ok {
		return fmt.Errorf("%s exists", name)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f[name] = b
	return nil
}

type fakeRecords map[string]Record

func (f fakeRecords) PutRecord(_ context.Context, id string, r Record) error {
	f[id] = r
	return nil
}

type fakeAgent struct {
	calls  int
	prompt string
	edit   func(dir string) error
	events string
	err    error
}

func (a *fakeAgent) Run(_ context.Context, dir, prompt string, stdout, _ io.Writer) error {
	a.calls++
	a.prompt = prompt
	if a.edit != nil {
		if err := a.edit(dir); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(stdout, a.events); err != nil {
		return err
	}
	return a.err
}

// reports holds every summary a build reported; a build must report exactly one.
type reports []Summary

func (r *reports) last(t *testing.T) Summary {
	t.Helper()
	if len(*r) != 1 {
		t.Fatalf("%d summaries reported, want 1", len(*r))
	}
	s := (*r)[0]
	if !buildEndings[ending{s.Outcome, s.StopReason, s.Phase}] {
		t.Fatalf("reported %s/%s at %q, which the record job rejects", s.Outcome, s.StopReason, s.Phase)
	}
	return s
}

func testDeps(projections fakeObjects, agent *fakeAgent) (BuildDeps, fakeObjects, *reports) {
	completions, reported := fakeObjects{}, &reports{}
	return BuildDeps{
		Projections: projections,
		Completions: completions,
		Agent:       agent,
		Report: func(s Summary) error {
			*reported = append(*reported, s)
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    time.Now,
	}, completions, reported
}

func testConfig(t *testing.T, repo string) BuildConfig {
	return BuildConfig{
		RecordID: "42-1",
		Repo:     repo,
		TempDir:  t.TempDir(),
		Pointer:  DefaultPointer,
		Model:    "p/m",
		Ticket:   Ticket{ID: "t-1", Size: "S", SizedBy: "hardcoded", Body: "Add a file."},
	}
}

func TestBuildFailsClosedWithoutAProjection(t *testing.T) {
	projectionPath := "projections/" + testSHA + "/" + buildProjectionFile
	tests := []struct {
		name       string
		objects    fakeObjects
		wantReason StopReason
	}{
		{"missing pointer", fakeObjects{projectionPath: []byte("rules " + ticketSentinel)}, StopProjectionMissing},
		{"missing projection", fakeObjects{DefaultPointer: []byte(testSHA + "\n")}, StopProjectionMissing},
		{"pointer is not a sha", fakeObjects{DefaultPointer: []byte("main")}, StopProjectionInvalid},
		{"projection without a sentinel", fakeObjects{DefaultPointer: []byte(testSHA), projectionPath: []byte("rules")},
			StopProjectionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, completions, reported := testDeps(tt.objects, agent)
			c := testConfig(t, t.TempDir())

			_, err := Build(context.Background(), deps, c)
			if err == nil {
				t.Fatal("Build succeeded without a projection")
			}
			if agent.calls != 0 {
				t.Fatalf("agent ran %d times, want 0", agent.calls)
			}
			if len(completions) != 0 {
				t.Fatalf("completions uploaded: %v", completions)
			}
			sum := reported.last(t)
			if sum.Outcome != OutcomeStopped || sum.StopReason != tt.wantReason || sum.Phase != PhaseProjection {
				t.Fatalf("summary = %s/%s at %s, want stopped/%s at projection",
					sum.Outcome, sum.StopReason, sum.Phase, tt.wantReason)
			}
		})
	}
}

func TestBuildCommitsAndBundlesTheAgentsEdits(t *testing.T) {
	repo := initRepo(t)
	objects := fakeObjects{
		DefaultPointer: []byte(testSHA + "\n"),
		"projections/" + testSHA + "/" + buildProjectionFile: []byte("# Rules\n\n# The ticket\n\n" + ticketSentinel + "\n"),
	}
	agent := &fakeAgent{
		edit: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
		},
		events: `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"cache":{"read":90}},"cost":0.5}}` + "\n",
	}
	deps, completions, reported := testDeps(objects, agent)
	c := testConfig(t, repo)

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Branch != "wingman/t-1-42-1" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.HasPrefix(agent.prompt, "# Rules") || !strings.HasSuffix(strings.TrimSpace(agent.prompt), "Add a file.") {
		t.Fatalf("prompt order wrong: %q", agent.prompt)
	}

	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt || rec.RuleStackSHA != testSHA || rec.Branch != res.Branch {
		t.Fatalf("summary = %+v", rec)
	}
	if !slices.Equal(rec.EditedFiles, []string{"version.go"}) {
		t.Fatalf("edited files = %q", rec.EditedFiles)
	}
	if rec.DiffLines.Added == 0 {
		t.Fatalf("diff lines = %+v, want the added file counted", rec.DiffLines)
	}
	if rec.Tokens.Input != 10 || rec.Tokens.CacheRead != 90 || rec.Model != "p/m" {
		t.Fatalf("summary usage = %+v, model %q", rec.Tokens, rec.Model)
	}
	if got := completions[rec.CompletionsObject]; !bytes.Equal(got, []byte(agent.events)) {
		t.Fatalf("completions object %q = %q", rec.CompletionsObject, got)
	}

	clone := t.TempDir()
	mustGit(t, clone, "clone", "-q", repo, ".")
	mustGit(t, clone, "fetch", "-q", res.BundlePath, res.Branch+":"+res.Branch)
	if files := mustGit(t, clone, "diff", "--name-only", "HEAD", res.Branch); files != "version.go\n" {
		t.Fatalf("bundle carries %q", files)
	}
}

func TestBuildReportsAFailedAgent(t *testing.T) {
	objects := fakeObjects{
		DefaultPointer: []byte(testSHA),
		"projections/" + testSHA + "/" + buildProjectionFile: []byte("# Rules\n" + ticketSentinel),
	}
	agent := &fakeAgent{events: "{}\n", err: errors.New("exit status 1")}
	deps, completions, reported := testDeps(objects, agent)
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with a failed agent")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s", rec.Outcome, rec.StopReason)
	}
	if _, ok := completions[rec.CompletionsObject]; !ok {
		t.Fatal("completions of a failed agent were not uploaded")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func validObjects() fakeObjects {
	return fakeObjects{
		DefaultPointer: []byte(testSHA),
		"projections/" + testSHA + "/" + buildProjectionFile: []byte("# Rules\n" + ticketSentinel),
	}
}

func TestBuildStopsOnAnInvalidModel(t *testing.T) {
	for _, model := range []string{"", "big-pickle", "opencode/big pickle", "-x/y", "opencode/big-pickle;rm"} {
		t.Run(model, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Model = model
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build accepted the model")
			}
			if agent.calls != 0 || reported.last(t).StopReason != StopModelInvalid {
				t.Fatalf("calls %d, reason %s", agent.calls, reported.last(t).StopReason)
			}
		})
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc…"},
		{"éé", 3, "é…"},
		{"a\xffb", 5, "a\uFFFDb"},
	} {
		got := truncate(tc.in, tc.n)
		if got != tc.want || !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestModelPatternAcceptsProviderIDs(t *testing.T) {
	for _, model := range []string{"opencode/big-pickle", "openrouter/deepseek/deepseek-v4:free", "opencode-go/glm-5.3-flash"} {
		if !modelPattern.MatchString(model) {
			t.Errorf("rejected %q", model)
		}
	}
}

type blockingAgent struct{}

func (blockingAgent) Run(ctx context.Context, _, _ string, stdout, _ io.Writer) error {
	if _, err := io.WriteString(stdout, "{}\n"); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestBuildStopsTheAgentAtItsDeadline(t *testing.T) {
	deps, completions, reported := testDeps(validObjects(), nil)
	deps.Agent = blockingAgent{}
	c := testConfig(t, initRepo(t))
	c.AgentTimeout = 100 * time.Millisecond

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded past the agent deadline")
	}
	rec := reported.last(t)
	if rec.StopReason != StopAgentTimeout {
		t.Fatalf("reason = %s", rec.StopReason)
	}
	if _, ok := completions[rec.CompletionsObject]; !ok {
		t.Fatal("completions were not uploaded after the deadline")
	}
}

func TestBuildUploadsCompletionsEvenWhenUsageCannotBeSummed(t *testing.T) {
	old := maxEventLine
	maxEventLine = 16
	t.Cleanup(func() { maxEventLine = old })
	longLine := strings.Repeat("x", 64) + "\n"

	tests := []struct {
		name       string
		agentErr   error
		wantReason StopReason
	}{
		{"agent succeeded", nil, ""},
		{"agent failed too", errors.New("exit status 1"), StopAgentExit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := &fakeAgent{events: longLine, err: tt.agentErr}
			deps, completions, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))

			_, _ = Build(context.Background(), deps, c)
			rec := reported.last(t)
			if got := completions[rec.CompletionsObject]; string(got) != longLine {
				t.Fatalf("completions = %q", got)
			}
			if rec.UsageWarning == "" || rec.StopReason != tt.wantReason {
				t.Fatalf("warning %q, reason %q", rec.UsageWarning, rec.StopReason)
			}
		})
	}
}

func TestBuildRefusesToBundleASecret(t *testing.T) {
	const secret = "sk-live-abcdef"
	agent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "cfg.go"), []byte(`var k = "`+secret+`"`), 0o600)
	}}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Secret = secret

	res, err := Build(context.Background(), deps, c)
	if err == nil || res.Changed {
		t.Fatal("Build bundled a branch holding the secret")
	}
	rec := reported.last(t)
	if rec.StopReason != StopSecretInBranch || strings.Contains(rec.StopDetail, secret) {
		t.Fatalf("reason %s, detail %q", rec.StopReason, rec.StopDetail)
	}
	if _, err := os.Stat(filepath.Join(c.TempDir, "wingman.bundle")); err == nil {
		t.Fatal("a bundle was written")
	}
}

type panickingAgent struct{}

func (panickingAgent) Run(context.Context, string, string, io.Writer, io.Writer) error {
	panic("boom")
}

func TestBuildReportsAPanicThenRepanics(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), nil)
	deps.Agent = panickingAgent{}
	c := testConfig(t, initRepo(t))

	defer func() {
		if recover() == nil {
			t.Fatal("Build swallowed the panic")
		}
		rec := reported.last(t)
		if rec.Outcome != OutcomeInfraFailure || rec.StopReason != StopPanic {
			t.Fatalf("record = %s/%s", rec.Outcome, rec.StopReason)
		}
	}()
	_, _ = Build(context.Background(), deps, c)
}

func TestBranchNameMatchesThePRJobsPattern(t *testing.T) {
	pattern := regexp.MustCompile(`^wingman/tracer-1-42-[0-9]+$`)
	if got := BranchName(Tracer.ID, "42-3"); !pattern.MatchString(got) {
		t.Fatalf("branch %q does not match the pr job's pattern", got)
	}
}

func TestBuildDependsOnNoRecordStore(t *testing.T) {
	store := reflect.TypeFor[RecordStore]()
	deps := reflect.TypeFor[BuildDeps]()
	for f := range deps.Fields() {
		if f.Type.Implements(store) {
			t.Errorf("BuildDeps.%s can write run records", f.Name)
		}
	}
}

func TestBuildReportsASummaryTheRecordJobAccepts(t *testing.T) {
	agent := &fakeAgent{
		edit: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
		},
		events: `{"type":"step_finish","part":{"tokens":{"input":10,"output":2},"cost":0.5}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	raw, err := reported.last(t).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(raw, "\r\n") {
		t.Fatalf("summary spans lines: %q", raw)
	}
	got, err := ParseSummary(raw, c.RecordID, c.Ticket, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomeBuilt || got.Branch != BranchName(c.Ticket.ID, c.RecordID) || got.Tokens.Cost != 0.5 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestBuildFailsWhenTheSummaryCannotBeReported(t *testing.T) {
	deps, _, _ := testDeps(validObjects(), &fakeAgent{})
	deps.Report = func(Summary) error { return errors.New("GITHUB_OUTPUT unwritable") }
	_, err := Build(context.Background(), deps, testConfig(t, initRepo(t)))
	if !errors.Is(err, ErrSummaryUnreported) {
		t.Fatalf("err = %v, want it marked unreported", err)
	}

	deps, _, _ = testDeps(fakeObjects{}, &fakeAgent{})
	deps.Report = func(Summary) error { return errors.New("GITHUB_OUTPUT unwritable") }
	_, err = Build(context.Background(), deps, testConfig(t, initRepo(t)))
	var stopped *StopError
	if !errors.As(err, &stopped) || !errors.Is(err, ErrSummaryUnreported) {
		t.Fatalf("err = %v, want a stop marked unreported", err)
	}
}
