//go:build unix

package runner

import (
	"context"
	"testing"
)

func TestHardenedRunCommandRunsDockerInItsOwnProcessGroup(t *testing.T) {
	cmd := hardenedRunCommand(context.Background(), hardenedRun{Name: "n", Workdir: "/w", Image: "img"})
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("the docker client does not get its own process group, so a cancel signals the runner's")
	}
}
