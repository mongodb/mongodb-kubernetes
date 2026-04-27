// mck-ci is the MongoDB Controllers for Kubernetes (MCK) CI/release helper.
//
// This is a thin entry point: it sets up signal handling and delegates to
// internal/ci/cli. All runnable logic lives under internal/ci so it can be
// imported from tests and so it never ends up linked into the operator binary.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := cli.NewRoot().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
