package runner

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// planFileListInstruction tells the plan agent the exact format its final
// message must end with, since nothing external defines a plan artifact format.
const planFileListInstruction = "Do not edit any files or run any shell commands: this phase is planning only.\n\n" +
	"End your final response with a fenced code block listing every file you plan to add or change, " +
	"one repository-relative path per line and nothing else in the block. Include the test file " +
	"alongside each source file you expect it to need — if the build edits a file this list does not " +
	"name, its PR opens as a draft that lists those files:\n\n" +
	"```plan-files\npath/one.go\npath/one_test.go\npath/two.go\n```"

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
