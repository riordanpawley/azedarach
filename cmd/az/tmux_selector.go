package main

import (
	"fmt"
	"os"

	"github.com/riordanpawley/azedarach/internal/config"
	tmuxselector "github.com/riordanpawley/azedarach/internal/tmuxselector"
)

func runGlobalTmuxSelector(cfg *config.Config) {
	if err := tmuxselector.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
