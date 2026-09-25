//go:build !unix

package runner

import "os/exec"

func ownProcessGroup(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) {}
