package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/aoagents/agent-orchestrator/accounts-manager/runner/internal/runner"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(runner.RunCLI(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
