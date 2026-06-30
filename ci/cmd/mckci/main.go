// mckci is the MongoDB Controllers for Kubernetes (MCK) CI tooling entry point.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := cli.NewRoot().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
