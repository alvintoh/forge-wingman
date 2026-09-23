// Command runner executes one run inside a repository's GitHub Actions workflow.
package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.New(slog.NewJSONHandler(os.Stdout, nil)).Info("runner: not implemented")
}
