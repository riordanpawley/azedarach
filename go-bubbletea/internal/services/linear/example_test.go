package linear_test

import (
	"context"
	"log/slog"
	"os"

	"github.com/riordanpawley/azedarach/internal/services/linear"
)

// Example shows how to use the Linear client with real command execution
func Example() {
	// Create a logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Create client with real command runner
	runner := &linear.ExecRunner{}
	client := linear.NewClient(runner, logger)

	// Fetch tasks
	ctx := context.Background()
	tasks, err := client.List(ctx)
	if err != nil {
		logger.Error("failed to list tasks", "error", err)
		return
	}

	logger.Info("fetched tasks", "count", len(tasks))
}
