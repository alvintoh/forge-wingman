package main

import (
	"fmt"
	"os"

	"acctsvc/internal/collect"
	"acctsvc/internal/store"
)

func main() {
	st := store.New()
	cfg := collect.DefaultConfig()
	// Local pacing for this demo run only. DefaultConfig deliberately leaves
	// Burst unset; this value is not it, and must not be read as the upstream's.
	cfg.Limiter.Burst = 2
	c := collect.NewCollector(cfg, st)
	inbound := []string{"ada@example.com", "grace@example.com", "alan@example.com"}
	names, err := c.Run(inbound)
	if err != nil {
		fmt.Fprintln(os.Stderr, "collector:", err)
		os.Exit(1)
	}
	for _, n := range names {
		fmt.Println(n)
	}
}
