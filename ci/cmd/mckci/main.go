// mckci is the MongoDB Controllers for Kubernetes (MCK) developer tooling
// entry point. The current scope is release automation under `mckci release`;
// other domains (CI, Evergreen, etc.) may register top-level subcommands later.
//
// This is a thin entry point: it sets up signal handling and delegates to
// ci/internal/cli. All runnable logic lives under ci/internal so it can be
// imported from tests and so it never ends up linked into the operator binary.
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
