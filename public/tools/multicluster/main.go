package main

import (
	"context"

	"github.com/mongodb/mongodb-kubernetes/multi/cmd"
)

func main() {
	ctx := context.Background()
	cmd.Execute(ctx)
}
