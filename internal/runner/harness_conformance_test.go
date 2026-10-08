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
	"time"

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
	// "readonly", "badkey") with conformanceKey as its key, returning the
	// invocations it records and its HOME.
	New func(t *testing.T, fixture string) (Harness, func() []harnessInvocation, string)
	// CredentialPath is the file under HOME the adapter writes its key to,
	// relative to HOME, deleted after each attempt (FR-11); empty for an
	// adapter that writes none.
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
	// LimitByMarker is set for a CLI with no limit exit code, which reports the
	// limit only in its error text; LimitExit is then unused.
	LimitByMarker bool
	// Rates is the model's rate card as the plan prices it, which the runner
	// meters from when the harness's own figure is absent or wrong (FR-22).
	Rates providers.Rates
}

// conformanceKey is the key every case's adapter runs on, distinct enough that
// finding it in a file under HOME means the adapter wrote it there.
const conformanceKey = "conformance-key-6d1f2a"

// runHarnessConformance runs the one contract every adapter must pass before a
// plan may select it: the prompts it takes, the profile that refuses to edit,
// its session continuity, the events it translates to the runner's shape, the
// tokens the runner meters from, the exits it classifies, and the key it
// leaves nowhere on disk.
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

	t.Run("a read-only profile refuses an edit, a new file and shell", func(t *testing.T) {
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
		if err := scanEvents(strings.NewReader(out.String()), func(e event) {
			if e.Type == "step_finish" && e.Part.Time == 0 {
				t.Error("a step carries no time, so it prices at peak")
			}
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cost is tokens times the plan's rates", func(t *testing.T) {
		// Twice the case's rates, so a harness figure that happens to match the
		// plan's own card cannot pass for a repricing.
		card := hc.Rates
		card.Input, card.Output, card.CacheRead, card.CacheWrite = 2*card.Input, 2*card.Output, 2*card.CacheRead, 2*card.CacheWrite
		plan := func(model string) (providers.Rates, bool) { return card, model == hc.Model }
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
				// Every step prices between the card's off-peak and peak rates,
				// one figure when the card has no peak (FR-22).
				offPeak := providers.Rates{Input: card.Input, Output: card.Output, CacheRead: card.CacheRead, CacheWrite: card.CacheWrite}
				low := offPeak.CostUSD(raw.Input, raw.Output, raw.CacheRead, raw.CacheWrite)
				high := card.At(time.Time{}).CostUSD(raw.Input, raw.Output, raw.CacheRead, raw.CacheWrite)
				if low <= 0 {
					t.Fatal("the case supplies no plan rates, so repricing is unproven")
				}
				got, err := meterUsage(strings.NewReader(out.String()), hc.Model, plan)
				if err != nil {
					t.Fatal(err)
				}
				if got.Cost < low-1e-12 || got.Cost > high+1e-12 {
					t.Fatalf("priced cost = %v, want tokens x the plan's rates, within [%v, %v]", got.Cost, low, high)
				}
				if name == "figure present" && approxEqual(raw.Cost, got.Cost) {
					t.Fatalf("the harness figure %v survived the plan's pricing", raw.Cost)
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
		code, stderrTail := hc.LimitExit, ""
		switch {
		case hc.LimitByMarker:
			markers := providers.AllowanceMarkers()
			if len(markers) == 0 {
				t.Fatal("no allowance marker is configured, so a limit reported in text cannot be classified")
			}
			code, stderrTail = 1, markers[0]
		case code == 0:
			t.Fatal("the case names no limit exit code and does not report the limit by marker")
		}
		err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
		if outcome, reason := h.Classify(stderrTail, err); outcome != OutcomeBudgetStop || reason != StopAllowanceExhausted {
			t.Fatalf("exit %d with stderr %q classified %s/%s, want the allowance stop", code, stderrTail, outcome, reason)
		}
	})

	t.Run("the key is not left on disk after each attempt", func(t *testing.T) {
		for _, fixture := range []string{"normal", "badkey"} {
			t.Run(fixture, func(t *testing.T) {
				h, _, home := hc.New(t, fixture)
				// The attempt may succeed or fail; the key must not outlive
				// either (FR-11).
				err := h.Agent(ProfileBuild, hc.Model).Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard)
				if wantsSuccess := fixture == "normal"; (err == nil) != wantsSuccess {
					t.Fatalf("the %s fixture: err = %v, want success %v", fixture, err, wantsSuccess)
				}
				if hc.CredentialPath != "" {
					if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(hc.CredentialPath))); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("the credential outlived the %s attempt (stat err %v)", fixture, err)
					}
				}
				err = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					b, err := os.ReadFile(path)
					if err == nil && strings.Contains(string(b), conformanceKey) {
						t.Errorf("the key outlived the %s attempt in %s", fixture, path)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}

// conformanceCases holds each adapter's entry to the suite, added from its own
// test file's init. A harness the registry exposes with no entry here fails
// TestEveryRegisteredHarnessPassesTheConformanceSuite, so it cannot be selected.
var conformanceCases = map[string]harnessCase{}

// addConformanceCase adds an adapter's case, panicking on a name already added.
func addConformanceCase(hc harnessCase) {
	if _, ok := conformanceCases[hc.Name]; ok {
		panic("conformance case " + hc.Name + " is added twice")
	}
	conformanceCases[hc.Name] = hc
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
