package runner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// harnessInvocation is one fake-CLI invocation an adapter recorded, in the
// standard shape the conformance suite asserts on whatever the harness.
type harnessInvocation struct {
	Args  []string
	Stdin string
}

// harnessCase is what one adapter supplies to the shared conformance suite: how
// to build it wired to a fake CLI replaying a recorded fixture, and the
// vocabulary its command line uses for the properties the suite checks.
type harnessCase struct {
	// Name is the harness's own name, matching its adapter's Name.
	Name string
	// Model is a well-formed model id the adapter accepts.
	Model string
	// New builds the adapter wired to a fake CLI replaying fixture ("normal",
	// "readonly", "badkey"), returning the invocations it records and its HOME,
	// where the credential is written.
	New func(t *testing.T, fixture string) (Harness, func() []harnessInvocation, string)
	// CredentialPath is the file under HOME the adapter writes its key to,
	// relative to HOME, deleted after each attempt (FR-11).
	CredentialPath string
	// ResumeFlag names the argument the adapter passes to continue a session
	// across rounds (FR-28).
	ResumeFlag string
	// ReadOnlyFlag is the argument the adapter passes only for a read-only
	// profile, BuildFlag the one it passes only for the build profile (FR-3).
	ReadOnlyFlag string
	BuildFlag    string
	// LimitExit is the adapter's documented allowance-limit exit code.
	LimitExit int
	// Rates is the model's rate card as the plan prices it, which the runner
	// meters from when the harness's own figure is absent or wrong (FR-22).
	Rates providers.Rates
}

// runHarnessConformance runs the one contract every adapter must pass before a
// plan may select it: the prompts it takes, the profile that refuses to edit,
// its session continuity, the events it translates to the runner's shape, the
// tokens the runner meters from, the exits it classifies, and the credential it
// deletes.
func runHarnessConformance(t *testing.T, hc harnessCase) {
	t.Helper()

	t.Run("the prompt travels on stdin", func(t *testing.T) {
		h, runs, _ := hc.New(t, "normal")
		prompt := strings.Repeat("rule line\n", 20000)
		var out, errBuf strings.Builder
		if err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), t.TempDir(), "", prompt, "", &out, &errBuf); err != nil {
			t.Fatal(err)
		}
		got := runs()
		if len(got) != 1 {
			t.Fatalf("the harness ran %d times, want 1", len(got))
		}
		if got[0].Stdin != prompt {
			t.Errorf("stdin carries %d bytes, want the %d-byte prompt", len(got[0].Stdin), len(prompt))
		}
		if slices.Contains(got[0].Args, prompt) {
			t.Error("the prompt travels as an argument, which the kernel caps")
		}
	})

	t.Run("a read-only profile refuses edit and shell", func(t *testing.T) {
		h, runs, _ := hc.New(t, "readonly")
		res, err := PlanSmoke(context.Background(), h.Agent(ProfilePlan, hc.Model), hc.Model, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Refused {
			t.Fatalf("the read-only profile left an effect: %s", res.Detail)
		}
		got := runs()
		if len(got) != 1 {
			t.Fatalf("the harness ran %d times, want 1", len(got))
		}
		if !slices.Contains(got[0].Args, hc.ReadOnlyFlag) || slices.Contains(got[0].Args, hc.BuildFlag) {
			t.Fatalf("the read-only run's args %v lack %q or carry %q", got[0].Args, hc.ReadOnlyFlag, hc.BuildFlag)
		}
	})

	t.Run("a resume keeps one session", func(t *testing.T) {
		h, runs, _ := hc.New(t, "normal")
		dir := t.TempDir()
		var first strings.Builder
		if err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), dir, "", "p", "", &first, io.Discard); err != nil {
			t.Fatal(err)
		}
		session, err := SessionID(strings.NewReader(first.String()))
		if err != nil || session == "" {
			t.Fatalf("the first run reported no session (err %v)", err)
		}
		var second strings.Builder
		if err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), dir, session, "p", "", &second, io.Discard); err != nil {
			t.Fatal(err)
		}
		got := runs()
		if len(got) != 2 {
			t.Fatalf("the harness ran %d times, want 2", len(got))
		}
		if slices.Contains(got[0].Args, hc.ResumeFlag) {
			t.Errorf("round 1 resumes a session it never had: %v", got[0].Args)
		}
		if i := slices.Index(got[1].Args, hc.ResumeFlag); i < 0 || i+1 >= len(got[1].Args) || got[1].Args[i+1] != session {
			t.Errorf("round 2 args %v do not resume %q", got[1].Args, session)
		}
	})

	t.Run("events translate with token usage", func(t *testing.T) {
		h, _, _ := hc.New(t, "normal")
		var out strings.Builder
		if err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), t.TempDir(), "", "p", "", &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		u, err := SumUsage(strings.NewReader(out.String()))
		if err != nil {
			t.Fatal(err)
		}
		if u.Steps == 0 || u.Input == 0 {
			t.Errorf("usage %+v carries no model step", u)
		}
		if text, _ := FinalText(strings.NewReader(out.String())); text == "" {
			t.Error("the translated events carry no final text")
		}
		if id, _ := SessionID(strings.NewReader(out.String())); id == "" {
			t.Error("the translated events carry no session id")
		}
	})

	t.Run("cost is tokens times the plan's rates", func(t *testing.T) {
		for name, fixture := range map[string]string{"figure absent": "unpriced", "figure present": "normal"} {
			t.Run(name, func(t *testing.T) {
				h, _, _ := hc.New(t, fixture)
				var out strings.Builder
				if err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), t.TempDir(), "", "p", "", &out, io.Discard); err != nil {
					t.Fatal(err)
				}
				raw, err := SumUsage(strings.NewReader(out.String()))
				if err != nil {
					t.Fatal(err)
				}
				if raw.Input == 0 || raw.Output == 0 {
					t.Fatalf("the run carries no tokens to meter: %+v", raw)
				}
				want := hc.Rates.CostUSD(raw.Input, raw.Output, raw.CacheRead, raw.CacheWrite)
				if want <= 0 {
					t.Fatal("the case supplies no plan rates, so repricing is unproven")
				}
				// The runner prices the run from the plan's rates, so a harness
				// figure that is absent or wrong is replaced by tokens times
				// those rates (FR-22).
				if got := priceUsage(raw, hc.Rates); !approxEqual(got.Cost, want) {
					t.Fatalf("priced cost = %v, want tokens x the plan's rates = %v", got.Cost, want)
				}
				if name == "figure present" && approxEqual(raw.Cost, want) {
					t.Fatalf("the fixture's harness figure %v already equals the plan's rates, so repricing is not shown", raw.Cost)
				}
			})
		}
	})

	t.Run("a bad key classifies as an agent failure", func(t *testing.T) {
		h, _, _ := hc.New(t, "badkey")
		var out, errBuf strings.Builder
		err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf)
		if err == nil {
			t.Fatal("the bad-key run reported success")
		}
		if outcome, reason := h.Classify(errBuf.String(), err); outcome != OutcomeAgentFailed || reason != StopAgentExit {
			t.Fatalf("classified %s/%s, want the ordinary agent failure", outcome, reason)
		}
	})

	t.Run("a limit exit classifies as a budget stop", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no sh to produce an exit code")
		}
		h, _, _ := hc.New(t, "normal")
		err := exec.Command("sh", "-c", "exit "+strconv.Itoa(hc.LimitExit)).Run()
		if outcome, reason := h.Classify("", err); outcome != OutcomeBudgetStop || reason != StopAllowanceExhausted {
			t.Fatalf("exit %d classified %s/%s, want the allowance stop", hc.LimitExit, outcome, reason)
		}
	})

	t.Run("the credential is deleted after each attempt", func(t *testing.T) {
		for _, fixture := range []string{"normal", "badkey"} {
			t.Run(fixture, func(t *testing.T) {
				h, _, home := hc.New(t, fixture)
				// The attempt may succeed or fail; the key must not outlive
				// either (FR-11).
				err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard)
				if wantsSuccess := fixture == "normal"; (err == nil) != wantsSuccess {
					t.Fatalf("the %s fixture: err = %v, want success %v", fixture, err, wantsSuccess)
				}
				if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(hc.CredentialPath))); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("the credential outlived the %s attempt (stat err %v)", fixture, err)
				}
			})
		}
	})
}

// TestEveryRegisteredHarnessPassesTheConformanceSuite is the selection gate: a
// plan selects a harness only from the registry, and an adapter with no passing
// conformance case here fails this test, so it can never be selected.
func TestEveryRegisteredHarnessPassesTheConformanceSuite(t *testing.T) {
	registered := Harnesses(func(string) string { return "a-key" })
	if len(registered) == 0 {
		t.Fatal("no harness is registered")
	}
	for _, h := range registered {
		hc, ok := conformanceCases[h.Name()]
		if !ok {
			t.Fatalf("harness %q is registered but has no conformance case: it must not be selectable", h.Name())
		}
		t.Run(h.Name(), func(t *testing.T) { runHarnessConformance(t, hc) })
	}
}
