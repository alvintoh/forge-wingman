package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// commandCodeProvider is the model-id prefix the Command Code CLI serves.
const commandCodeProvider = "command-code"

// commandCodeMaxTurns bounds a run's own turns; a run that reaches it exits 8,
// which Classify records as an ordinary agent failure.
const commandCodeMaxTurns = 200

// commandCodeBin is the proprietary agent CLI's binary (PACKAGE.txt), and
// commandCodeKeyEnv names the environment variable it reads its API key from.
const (
	commandCodeBin    = "cmd"
	commandCodeKeyEnv = "COMMANDCODE_API_KEY"
)

// Harnesses returns the conformant agent harnesses a run may route to, from the
// environment — the composition root's own construction, kept here so the
// vendor stays named only in its adapter.
func Harnesses(getenv func(string) string) []Harness {
	var home string
	if tmp := getenv("RUNNER_TEMP"); tmp != "" {
		home = filepath.Join(tmp, "commandcode-home")
	}
	return provenHarnesses([]Harness{CommandCodeHarness{Bin: commandCodeBin, Key: getenv(commandCodeKeyEnv), Home: home}})
}

// HarnessSecrets are the credentials the harnesses hold, which a run checks
// against a branch before it is pushed.
func HarnessSecrets(getenv func(string) string) []string {
	return []string{getenv(commandCodeKeyEnv)}
}

// commandCodeTranscripts is where the CLI writes its session transcripts,
// under HOME: one .jsonl per session, beside a .checkpoints.jsonl it ignores.
const commandCodeTranscripts = ".commandcode/projects"

// commandCodeAuthFile is where the CLI reads its credentials, under HOME; a
// saved login also uses it, so the adapter writes it rather than passing a key
// in the environment (PACKAGE.txt).
const commandCodeAuthFile = ".commandcode/auth.json"

// commandCodeMemoryFile is the user memory the CLI reads ahead of its per-run
// context block, under HOME. A harness writes the projection's rules head there
// so its large stable part precedes the context block (working directory,
// branch, date) that varies per run.
const commandCodeMemoryFile = ".commandcode/AGENTS.md"

// commandCodeSettingsFile is the CLI's settings file under HOME. The adapter
// disables the scratchpad section there, whose per-session path would otherwise
// change the system prompt on every run.
const commandCodeSettingsFile = ".commandcode/settings.json"

// CommandCodeHarness runs the Command Code CLI on its own key.
type CommandCodeHarness struct {
	Bin string
	Key string
	// Home is the CLI's HOME for the whole run: it holds the auth file and the
	// session transcripts a later round resumes.
	Home string
}

// Name is the harness's own name, the key a plan's configuration selects it by.
func (h CommandCodeHarness) Name() string { return commandCodeProvider }

// Conformant declares the adapter conformant; the shared conformance suite runs
// against it in CI.
func (h CommandCodeHarness) Conformant() error { return nil }

// Agent returns the Command Code agent for profile p.
func (h CommandCodeHarness) Agent(p Profile, model string) Agent {
	return CommandCodeAgent{Bin: h.Bin, Key: h.Key, Home: h.Home, Model: model, Profile: p}
}

// Ready reports the harness usable only when its key is present (AC7).
func (h CommandCodeHarness) Ready() error {
	if h.Key == "" {
		return errors.New("the command-code harness needs the COMMANDCODE_API_KEY secret")
	}
	return nil
}

// Classify classifies a failed run by the CLI's documented exit code first,
// then the assumed provider markers (AC3).
func (h CommandCodeHarness) Classify(stderrTail string, exitErr error) (Outcome, StopReason) {
	if code, ok := commandCodeExitCode(exitErr); ok {
		if f, ok := commandCodeFailure[code]; ok {
			return f.outcome, f.reason
		}
	}
	return classifyMarkers(stderrTail)
}

// commandCodeFailure maps the CLI's documented exit codes to what the run
// records (AC3): 5 (rate limit) and 10 (insufficient credits) to the allowance
// stop, 6 and 7 (network, server) to a model-unavailable infra stop so the
// next model is tried, the rest to an ordinary agent failure.
var commandCodeFailure = map[int]struct {
	outcome Outcome
	reason  StopReason
}{
	3:  {OutcomeAgentFailed, StopAgentExit},
	4:  {OutcomeAgentFailed, StopAgentExit},
	5:  {OutcomeBudgetStop, StopAllowanceExhausted},
	6:  {OutcomeInfraFailure, StopModelUnavailable},
	7:  {OutcomeInfraFailure, StopModelUnavailable},
	8:  {OutcomeAgentFailed, StopAgentExit},
	9:  {OutcomeAgentFailed, StopAgentExit},
	10: {OutcomeBudgetStop, StopAllowanceExhausted},
}

func commandCodeExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

// agentKillGrace is the CLI's WaitDelay: how long Wait lets it linger once its context ends.
const agentKillGrace = 30 * time.Second

// CommandCodeAgent runs the Command Code CLI for one profile.
type CommandCodeAgent struct {
	Bin     string
	Key     string
	Home    string
	Model   string
	Profile Profile
}

// WithModel returns a copy of a that runs model, keeping its profile.
func (a CommandCodeAgent) WithModel(model string) Agent { a.Model = model; return a }

// Run sends the prompt on stdin, since a projection outgrows the kernel's limit
// on one argument, and translates the CLI's frames to the runner's own event
// shape on stdout. rules, when non-empty, is written to the CLI's user memory
// file so it precedes the per-run context block; an empty rules removes that
// file, so a later round never inherits an earlier one's. A non-empty session is
// resumed from the transcript under Home; the auth file is removed after each
// run, so the key never sits on disk while the run's later steps do. The CLI's
// frames carry no cost, so the run's cost is read from the transcripts it wrote
// and emitted as one cost event.
func (a CommandCodeAgent) Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error {
	if a.Home == "" {
		return errors.New("the command-code harness has no home directory")
	}
	if err := writeCommandCodeAuth(a.Home, a.Key); err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(a.Home, filepath.FromSlash(commandCodeAuthFile))) }()
	if err := writeCommandCodeMemory(a.Home, rules); err != nil {
		return err
	}
	if err := writeCommandCodeSettings(a.Home); err != nil {
		return err
	}
	// The CLI takes its own model id — ours without the harness's prefix.
	model := strings.TrimPrefix(a.Model, commandCodeProvider+"/")
	args := []string{"-p", "--output-format", "json", "--model", model,
		"--max-turns", strconv.Itoa(commandCodeMaxTurns), "--skip-onboarding", "--no-auto-update"}
	if a.Profile == ProfileBuild {
		args = append(args, "--yolo")
	} else {
		args = append(args, "--plan")
	}
	if session != "" {
		args = append(args, "--resume", session)
	}
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	cmd.Dir = dir
	// os/exec keeps the last value for a duplicate key, so this HOME overrides
	// the job's own without the base set having to exclude it.
	cmd.Env = append(filterEnv(os.Environ(), agentBaseEnvNames), "HOME="+a.Home)
	cmd.Stdin = strings.NewReader(prompt)
	// Files, not buffers: os/exec pipes any other writer, and a background
	// child holding that pipe would stall Wait for the whole WaitDelay.
	frames, err := os.CreateTemp(a.Home, "frames-*")
	if err != nil {
		return fmt.Errorf("agent output file: %w", err)
	}
	defer func() { _ = frames.Close(); _ = os.Remove(frames.Name()) }()
	cmd.Stdout = frames
	errFile, ok := stderr.(*os.File)
	if !ok {
		if errFile, err = os.CreateTemp(a.Home, "stderr-*"); err != nil {
			return fmt.Errorf("agent output file: %w", err)
		}
		defer func() { _ = errFile.Close(); _ = os.Remove(errFile.Name()) }()
	}
	cmd.Stderr = errFile
	cmd.WaitDelay = agentKillGrace
	ownProcessGroup(cmd)
	before, beforeErr := commandCodeCostByID(a.Home)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agent run: %w", err)
	}
	runErr := cmd.Wait()
	killProcessGroup(cmd)
	cost := commandCodeRunCost(a.Home, before, beforeErr, runErr == nil)
	if !ok {
		if _, err := errFile.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("agent output file: %w", err)
		}
		if _, err := io.Copy(stderr, errFile); err != nil {
			return fmt.Errorf("agent stderr: %w", err)
		}
	}
	if _, err := frames.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("agent output file: %w", err)
	}
	// Translate whatever the CLI wrote, even on a failure, so the failed run's
	// usage and final text are still recorded.
	if err := translateCommandCodeFrames(frames, stdout, stderr); err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(cost); err != nil {
		return fmt.Errorf("agent cost event: %w", err)
	}
	if runErr != nil {
		return fmt.Errorf("agent run: %w", runErr)
	}
	return nil
}

// commandCodeRunCost is the event carrying the cost of the messages that
// appeared in home's transcripts since the before snapshot, or a usage_warning
// event when the transcripts could not be read. A run that failed and left no
// transcripts at all simply cost nothing.
func commandCodeRunCost(home string, before map[string]float64, beforeErr error, succeeded bool) event {
	var e event
	after, err := commandCodeCostByID(home)
	if err == nil && beforeErr != nil {
		err = beforeErr
	}
	if err == nil && after == nil && succeeded {
		err = errors.New("the run wrote no transcripts")
	}
	if err != nil {
		e.Type = "usage_warning"
		e.Warning = "transcript cost unavailable: " + err.Error()
		return e
	}
	ids := make([]string, 0, len(after))
	for id := range after {
		if _, seen := before[id]; !seen {
			ids = append(ids, id)
		}
	}
	// Sorted so the float sum is the same whatever order the map yields.
	sort.Strings(ids)
	e.Type = "cost"
	for _, id := range ids {
		e.Part.Cost += after[id]
	}
	return e
}

// commandCodeTranscriptLine is the part of a transcript line that bills: an
// assistant message's id and the cost the CLI recorded for it.
type commandCodeTranscriptLine struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Usage *struct {
		CostUSD *float64 `json:"costUsd"`
	} `json:"usage"`
}

// commandCodeCostByID returns the costUsd of every billed message in home's
// transcripts, keyed by message id; nil, with no error, when the CLI has
// written none yet.
//
// A resume copies the session's history into a new transcript under the same
// ids, so a message is kept once however many files carry it.
func commandCodeCostByID(home string) (map[string]float64, error) {
	root := filepath.Join(home, filepath.FromSlash(commandCodeTranscripts))
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	costs := map[string]float64{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".checkpoints.jsonl") {
			return nil
		}
		return readCommandCodeTranscript(path, costs)
	})
	if err != nil {
		return nil, fmt.Errorf("reading command code transcripts: %w", err)
	}
	return costs, nil
}

// readCommandCodeTranscript adds the billed messages in the transcript at path
// to costs, keeping the first cost seen for an id.
func readCommandCodeTranscript(path string, costs map[string]float64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	for {
		var line commandCodeTranscriptLine
		// A CLI killed mid-write leaves its last line cut short; what precedes it still counts.
		if err := dec.Decode(&line); errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if line.Type != "message" || line.ID == "" || line.Usage == nil || line.Usage.CostUSD == nil {
			continue
		}
		if _, seen := costs[line.ID]; !seen {
			costs[line.ID] = *line.Usage.CostUSD
		}
	}
}

// writeCommandCodeAuth writes the key the CLI reads from ~/.commandcode/auth.json.
func writeCommandCodeAuth(home, key string) error {
	path := filepath.Join(home, filepath.FromSlash(commandCodeAuthFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("command code auth directory: %w", err)
	}
	body, err := json.Marshal(struct {
		APIKey string `json:"apiKey"`
	}{key})
	if err != nil {
		return fmt.Errorf("command code auth file: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("command code auth file: %w", err)
	}
	return nil
}

// writeCommandCodeMemory writes rules to the CLI's user memory file, or removes
// it when rules is empty, so a round with no projection never inherits a
// previous round's from the run's shared HOME.
func writeCommandCodeMemory(home, rules string) error {
	path := filepath.Join(home, filepath.FromSlash(commandCodeMemoryFile))
	if rules == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("command code memory file: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("command code memory directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(rules), 0o600); err != nil {
		return fmt.Errorf("command code memory file: %w", err)
	}
	return nil
}

// writeCommandCodeSettings writes the CLI settings a run needs, disabling the
// scratchpad section whose per-session path would otherwise change the system
// prompt on every run.
func writeCommandCodeSettings(home string) error {
	path := filepath.Join(home, filepath.FromSlash(commandCodeSettingsFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("command code settings directory: %w", err)
	}
	body, err := json.Marshal(struct {
		DisableScratchpad bool `json:"disableScratchpad"`
	}{true})
	if err != nil {
		return fmt.Errorf("command code settings file: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("command code settings file: %w", err)
	}
	return nil
}

// commandCodeUsage is one turn's token counts, as the CLI reports them.
type commandCodeUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// commandCodeFrame is one line of the CLI's JSON output: a wrapped event, or
// the terminal result line.
type commandCodeFrame struct {
	Type      string          `json:"type"`
	Event     json.RawMessage `json:"event"`
	SessionID string          `json:"sessionId"`
	FinalText string          `json:"finalText"`
}

// commandCodeEvent is one event inside a frame.
type commandCodeEvent struct {
	Type      string                `json:"type"`
	SessionID string                `json:"sessionId"`
	Content   []commandCodeContent  `json:"content"`
	Usage     *commandCodeUsage     `json:"usage"`
	Error     *commandCodeErrorBody `json:"error"`
}

// commandCodeContent is one part of a finished message.
type commandCodeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type commandCodeErrorBody struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// commandCodeMetaEvent reports a session id in the runner's own event shape.
type commandCodeMetaEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
}

// commandCodeTextEvent is a text part in the runner's own event shape.
type commandCodeTextEvent struct {
	Type string `json:"type"`
	Part struct {
		Text string `json:"text"`
	} `json:"part"`
}

// commandCodeErrorEvent is an error part in the runner's own event shape.
type commandCodeErrorEvent struct {
	Type  string               `json:"type"`
	Error commandCodeErrorBody `json:"error"`
}

func commandCodeText(text string) commandCodeTextEvent {
	var e commandCodeTextEvent
	e.Type = "text"
	e.Part.Text = text
	return e
}

// commandCodeStepEvent shapes one turn's usage as the runner's own step_finish
// event; the CLI's per-request events carry no cost, so the run's cost arrives
// as one cost event.
func commandCodeStepEvent(u commandCodeUsage) event {
	var e event
	e.Type = "step_finish"
	e.Part.Tokens.Input = u.InputTokens
	e.Part.Tokens.Output = u.OutputTokens
	e.Part.Tokens.Cache.Read = u.CacheReadTokens
	e.Part.Tokens.Cache.Write = u.CacheWriteTokens
	return e
}

// translateCommandCodeFrames rewrites the CLI's frames as the runner's own
// event shape, so SumUsage, FinalText and SessionID need no harness-specific
// parser. A run_error's detail is also echoed to stderr, where Classify reads
// it (AC3); the CLI writes the result line's error there itself.
func translateCommandCodeFrames(r io.Reader, stdout, stderr io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	enc := json.NewEncoder(stdout)
	for sc.Scan() {
		var frame commandCodeFrame
		if json.Unmarshal(sc.Bytes(), &frame) != nil {
			continue
		}
		switch frame.Type {
		case "event":
			if err := emitCommandCodeEvent(enc, stderr, frame.Event); err != nil {
				return err
			}
		case "result":
			if frame.SessionID != "" {
				if err := enc.Encode(commandCodeMetaEvent{Type: "result", SessionID: frame.SessionID}); err != nil {
					return err
				}
			}
			// The result line's final text is authoritative, so the last text
			// event always holds it, even when an earlier message was longer.
			if err := enc.Encode(commandCodeText(frame.FinalText)); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func emitCommandCodeEvent(enc *json.Encoder, stderr io.Writer, raw json.RawMessage) error {
	var ev commandCodeEvent
	if json.Unmarshal(raw, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "run_start":
		if ev.SessionID != "" {
			return enc.Encode(commandCodeMetaEvent{Type: "run_start", SessionID: ev.SessionID})
		}
	case "message_end":
		for _, c := range ev.Content {
			if c.Type == "text" && c.Text != "" {
				if err := enc.Encode(commandCodeText(c.Text)); err != nil {
					return err
				}
			}
		}
	case "turn_end":
		if ev.Usage != nil {
			return enc.Encode(commandCodeStepEvent(*ev.Usage))
		}
	case "run_error":
		if ev.Error != nil {
			if _, err := fmt.Fprintf(stderr, "command-code error: %s: %s\n", ev.Error.Name, ev.Error.Message); err != nil {
				return err
			}
			return enc.Encode(commandCodeErrorEvent{Type: "error", Error: *ev.Error})
		}
	}
	return nil
}
