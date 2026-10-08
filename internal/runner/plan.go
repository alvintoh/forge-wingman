package runner

import (
	"errors"
	"fmt"
	"path"
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

// PlanPrompt is the plan phase's prompt split into the plan projection's rules
// head and the ticket text the plan-files format instruction is appended to.
func PlanPrompt(projection string, t Ticket) (rules, prompt string, err error) {
	rules, ticket, err := RenderPromptParts(projection, t)
	if err != nil {
		return "", "", err
	}
	return rules, ticket + "\n\n" + planFileListInstruction, nil
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
