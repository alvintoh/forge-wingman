package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// planFileListInstruction tells the plan agent the exact format its final
// message must end with, since nothing external defines a plan artifact format.
const planFileListInstruction = "Do not edit any files or run any shell commands: this phase is planning only.\n\n" +
	"End your final response with a fenced code block listing every file you plan to add or change, " +
	"one repository-relative path per line and nothing else in the block. Include the test file " +
	"alongside each source file you expect it to need — if the build edits a file this list does not " +
	"name, its PR opens as a draft that lists those files:\n\n" +
	"```plan-files\npath/one.go\npath/one_test.go\npath/two.go\n```"

// planDecisionInstruction is appended to the plan-files instruction for a guided
// plan: the phase that may ask the owner a decision it may not take alone, and
// how to phrase the request. The file list stays the default ending; the
// decision block replaces it only when the plan genuinely needs the owner.
const planDecisionInstruction = "But if — and only if — you need a decision you may not take alone, end your final response " +
	"with a fenced decision block instead of the file list. A decision is yours to ask only when it is outward-facing " +
	"(it reaches a third party), irreversible (it cannot be undone), a security choice, or scope (what the work includes " +
	"or leaves out). Do not ask about a local choice you can settle yourself from the repository and the evidence in front " +
	"of you — a reversible, in-repo decision is yours to make. When you ask, keep it to one question, at most three lines " +
	"of context, and two to four options; put your recommendation first, and give each option its cost:\n\n" +
	"```plan-decision\n" +
	"{\"question\": \"…\", \"context\": [\"…\", \"…\"], \"options\": [{\"label\": \"…\", \"value\": \"…\", \"cost\": \"…\"}]}\n" +
	"```"

// PlanPrompt is the plan phase's prompt split into the plan projection's rules
// head and the ticket text the plan-files format instruction is appended to. A
// guided plan — the plan stage's own, which can carry the owner's answer back —
// may ask a decision instead of naming files.
func PlanPrompt(projection string, t Ticket, guided bool) (rules, prompt string, err error) {
	rules, ticket, err := RenderPromptParts(projection, t)
	if err != nil {
		return "", "", err
	}
	instruction := planFileListInstruction
	if guided {
		instruction += "\n\n" + planDecisionInstruction
	}
	return rules, ticket + "\n\n" + instruction, nil
}

// errPlanFiles reports a plan agent's final message that carries no valid
// plan-files block.
var errPlanFiles = errors.New("plan agent's response carries no valid plan-files list")

var planFilesBlock = regexp.MustCompile("(?s)```plan-files\\s*\\n(.*?)```")

// planDecisionBlock is the fenced block a guided plan ends with when it asks
// the owner a decision: the decision's JSON, one object.
var planDecisionBlock = regexp.MustCompile("(?s)```plan-decision\\s*\\n(.*?)```")

// parsePlanArtifacts reads the plan agent's final message for the artifact a
// plan phase produces: the plan-files list, or — when guided, where the plan
// may ask the owner a decision instead — the plan-decision request. Exactly one
// may be present, and a plan-decision block in a phase that cannot carry an
// answer back is refused.
func parsePlanArtifacts(text string, guided bool) (files []string, decision *Decision, err error) {
	requests := planDecisionBlock.FindAllStringSubmatch(text, -1)
	if len(requests) == 0 {
		files, err := parsePlanFiles(text)
		return files, nil, err
	}
	if guided && planFilesBlock.MatchString(text) {
		return nil, nil, fmt.Errorf("%w: the plan carries both a file list and a decision", errPlanFiles)
	}
	if !guided {
		return nil, nil, fmt.Errorf("%w: this phase cannot ask the owner a decision", errDecisionInvalid)
	}
	d, err := parseDecision(requests[len(requests)-1][1])
	if err != nil {
		return nil, nil, err
	}
	return nil, d, nil
}

// parsePlanFiles reads the plan agent's final message for the last plan-files
// block and returns its paths, repository-relative and non-empty.
func parsePlanFiles(text string) ([]string, error) {
	matches := planFilesBlock.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, errPlanFiles
	}
	var files []string
	for _, line := range strings.Split(matches[len(matches)-1][1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !repoRelative(line) {
			return nil, fmt.Errorf("%w: %q is not a repository-relative path", errPlanFiles, truncate(line, logErrorLimit))
		}
		files = append(files, line)
	}
	if len(files) == 0 {
		return nil, errPlanFiles
	}
	return files, nil
}

// outOfPlanFiles returns the edited files absent from the plan's file list.
func outOfPlanFiles(edited, planned []string) []string {
	allowed := make(map[string]bool, len(planned))
	for _, f := range planned {
		allowed[f] = true
	}
	var extra []string
	for _, f := range edited {
		if !allowed[f] {
			extra = append(extra, f)
		}
	}
	return extra
}

// workflowDir is where GitHub reads workflows from; the run's GitHub App holds
// no permission to push a change there.
const workflowDir = ".github/workflows"

// errWorkflowChange reports a plan or build touching a file under workflowDir.
var errWorkflowChange = errors.New("touches files under " + workflowDir + "/, which the run cannot push")

// workflowFiles returns the paths that are workflowDir or sit under it.
func workflowFiles(paths []string) []string {
	var hits []string
	for _, p := range paths {
		if c := path.Clean(p); c == workflowDir || strings.HasPrefix(c, workflowDir+"/") {
			hits = append(hits, p)
		}
	}
	return hits
}

// workflowChange is errWorkflowChange naming files.
func workflowChange(files []string) error {
	return fmt.Errorf("%w: %s", errWorkflowChange, strings.Join(files, ", "))
}

// PlanStageResult is the plan stage's output for the trusted record job: the
// repository-relative files the plan names and the commit the plan was made
// against, or — when the plan asked the owner a decision instead — that
// decision. It travels as an artifact, since the model job's identity cannot
// write the run record.
type PlanStageResult struct {
	Files    []string  `json:"files"`
	BaseSHA  string    `json:"base_sha"`
	Decision *Decision `json:"decision,omitempty"`
}

// Check reports whether r is a usable plan stage result read back from the
// model job: either at least one file, each repository-relative and none under
// .github/workflows/, or one bounded decision — never both — and a commit sha
// for the base. The model job is untrusted, so the trusted record job re-checks
// its plan here before any record write.
func (r PlanStageResult) Check() error {
	switch {
	case r.Decision != nil && len(r.Files) > 0:
		return fmt.Errorf("%w: the plan names files and asks a decision", errPlanFiles)
	case r.Decision != nil:
		if err := r.Decision.Check(); err != nil {
			return err
		}
	case len(r.Files) == 0:
		return fmt.Errorf("%w: the plan names no files", errPlanFiles)
	default:
		for _, f := range r.Files {
			if !repoRelative(f) {
				return fmt.Errorf("%w: %q is not a repository-relative path", errPlanFiles, truncate(f, logErrorLimit))
			}
		}
		if wf := workflowFiles(r.Files); len(wf) > 0 {
			return workflowChange(wf)
		}
	}
	if !shaPattern.MatchString(r.BaseSHA) {
		return fmt.Errorf("plan base %q is not a commit sha", truncate(r.BaseSHA, logErrorLimit))
	}
	return nil
}

// EncodePlanStage renders r as the plan artifact's one-line JSON content.
func EncodePlanStage(r PlanStageResult) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParsePlanStage decodes a plan artifact the model job wrote and checks it.
func ParsePlanStage(raw []byte) (PlanStageResult, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r PlanStageResult
	if err := dec.Decode(&r); err != nil {
		return PlanStageResult{}, fmt.Errorf("decoding the plan: %w", err)
	}
	if dec.More() {
		return PlanStageResult{}, errors.New("trailing data after the plan")
	}
	if err := r.Check(); err != nil {
		return PlanStageResult{}, err
	}
	return r, nil
}

// PlanRecord is the plan stage's own data on a run row, written by the trusted
// record-plan-stage job and read by the ticket job that carries it to the build.
// It is kept apart from Record so Finalize — which rewrites every field Record
// names and keeps those it does not — leaves the plan intact.
type PlanRecord struct {
	Files     []string  `firestore:"plan_files"`
	BaseSHA   string    `firestore:"plan_base_sha"`
	SettledAt time.Time `firestore:"plan_settled_at"`
}
