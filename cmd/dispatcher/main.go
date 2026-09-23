// Command dispatcher is the scheduled job that polls Linear and dispatches runs.
package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.New(slog.NewJSONHandler(os.Stdout, nil)).Info("dispatcher: not implemented")
}
