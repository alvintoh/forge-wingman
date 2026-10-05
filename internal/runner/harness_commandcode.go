package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// commandCodeProvider is the model-id prefix the Command Code CLI serves.
const commandCodeProvider = "command-code"

// commandCodeMaxTurns bounds a run's own turns; a run that reaches it exits 8,
// which Classify records as an ordinary agent failure.
const commandCodeMaxTurns = 200

// commandCodeAuthFile is where the CLI reads its credentials, under HOME; a
// saved login also uses it, so the adapter writes it rather than passing a key
// in the environment (PACKAGE.txt).
const commandCodeAuthFile = ".commandcode/auth.json"

// CommandCodeHarness runs the Command Code CLI, a proprietary harness that
// needs an explicit opt-in and its own key.
type CommandCodeHarness struct {
	Bin   string
	Key   string
	OptIn bool
	// Home is the CLI's HOME for the whole run: it holds the auth file and the
	// session transcripts a later round resumes.
	Home string
}

// Providers are the model-id prefixes the Command Code CLI serves.
func (h CommandCodeHarness) Providers() []string { return []string{commandCodeProvider} }

// Agent returns the Command Code agent for profile p.
func (h CommandCodeHarness) Agent(p Profile, model string) Agent {
	return CommandCodeAgent{Bin: h.Bin, Key: h.Key, Home: h.Home, Model: model, Profile: p}
}

// Ready reports the harness usable only when the run opted in and its key is
// present (AC7).
func (h CommandCodeHarness) Ready() error {
	if !h.OptIn || h.Key == "" {
		return errors.New("the command-code harness needs COMMAND_CODE_OPT_IN=true and the COMMANDCODE_API_KEY secret")
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
// shape on stdout. A non-empty session is resumed from the transcript under
// Home.
func (a CommandCodeAgent) Run(ctx context.Context, dir, session, prompt string, stdout, stderr io.Writer) error {
	if a.Home == "" {
		return errors.New("the command-code harness has no home directory")
	}
	if err := writeCommandCodeAuth(a.Home, a.Key); err != nil {
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
	var frames bytes.Buffer
	cmd.Stdout = &frames
	cmd.Stderr = stderr
	cmd.WaitDelay = agentKillGrace
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agent run: %w", err)
	}
	runErr := cmd.Wait()
	killProcessGroup(cmd)
	// Translate whatever the CLI wrote, even on a failure, so the failed run's
	// usage and final text are still recorded.
	if err := translateCommandCodeFrames(&frames, stdout, stderr); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("agent run: %w", runErr)
	}
	return nil
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
// event; the CLI bills in credits, so cost is left zero.
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
