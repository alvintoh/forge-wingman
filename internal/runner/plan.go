package runner

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// planAgentName is the restricted opencode agent the plan phase runs under.
const planAgentName = "wingman-plan"

// planAgentConfig is OPENCODE_CONFIG_CONTENT for planAgentName: denying edit and
// bash disables both tools outright rather than merely gating them behind a
// prompt, confirmed against the installed opencode CLI's own resolved agent
// config (`opencode debug agent <name>`).
const planAgentConfig = `{"agent":{"` + planAgentName + `":{"mode":"primary","permission":{"edit":"deny","bash":"deny"}}}}`

// PlanOpencode is the restricted agent the plan phase runs: opencode with edit
// and bash denied, so it can read and reason but not touch the worktree.
func PlanOpencode(bin, model string) Opencode {
	return Opencode{Bin: bin, Model: model, Agent: planAgentName, ConfigContent: planAgentConfig}
}

// planFileListInstruction tells the plan agent the exact format its final
// message must end with, since nothing external defines a plan artifact format.
const planFileListInstruction = "Do not edit any files or run any shell commands: this phase is planning only.\n\n" +
	"End your final response with a fenced code block listing every file you plan to add or change, " +
	"one repository-relative path per line and nothing else in the block:\n\n" +
	"```plan-files\npath/one.go\npath/two.go\n```"

// PlanPrompt is the plan phase's prompt: the plan projection with the ticket
// substituted, followed by the plan-files format instruction.
func PlanPrompt(projection string, t Ticket) (string, error) {
	base, err := RenderPrompt(projection, t)
	if err != nil {
		return "", err
	}
	return base + "\n\n" + planFileListInstruction, nil
}

// errPlanFiles reports a plan agent's final message that carries no valid
// plan-files block.
var errPlanFiles = errors.New("plan agent's response carries no valid plan-files list")

var planFilesBlock = regexp.MustCompile("(?s)```plan-files\\s*\\n(.*?)```")

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
