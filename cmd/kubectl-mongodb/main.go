package main

import (
	"context"

	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/root"
)

func main() {
	ctx := context.Background()
	root.Execute(ctx)
}
