package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestWriteOutputsRefusesAMultilineValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := writeOutputs(path, map[string]string{"summary": "a\nb=c"}); err == nil {
		t.Fatal("wrote a value spanning lines")
	}
	if err := writeOutputs(path, map[string]string{"summary": `{"a":"b\nc"}`}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `summary={"a":"b\nc"}`+"\n" {
		t.Fatalf("GITHUB_OUTPUT = %q", got)
	}
}

func TestWriteSummaryWithNoOutputIsUnreported(t *testing.T) {
	if err := writeSummary("", runner.SetupSummary(errors.New("x"), time.Now())); !errors.Is(err, errNoOutput) {
		t.Fatalf("err = %v, want errNoOutput", err)
	}
}

func TestWriteMultilineOutputUsesADelimiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := writeMultilineOutput(path, "body", "a\nb"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	delim, ok := strings.CutPrefix(lines[0], "body<<")
	if !ok || len(lines) != 4 || lines[1] != "a" || lines[2] != "b" || lines[3] != delim {
		t.Fatalf("GITHUB_OUTPUT = %q", got)
	}
}

func TestAppendFileAppendsAndCreatesOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	for _, line := range []string{"a\n", "b\n"} {
		if err := appendFile(path, line); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "a\nb\n" {
		t.Fatalf("file = %q, err %v, want both appends", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, err %v, want 0600", info.Mode(), err)
	}
	if err := appendFile(t.TempDir(), "x"); err == nil {
		t.Fatal("appendFile wrote to a directory")
	}
}
