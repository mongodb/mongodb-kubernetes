package main

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDebug(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second*20)
	defer cancel()

	flags := flags{
		operatorClusterName: "gke_scratch-kubernetes-team_europe-central2-a_k8s-mdb-0",
		namespace:           "mongodb",
		operatorNamespace:   "mongodb-operator",
		typeParam:           "",
		name:                "",
		watch:               true,
		deployPods:          false,
	}
	require.NoError(t, debug(ctx, flags))
}
