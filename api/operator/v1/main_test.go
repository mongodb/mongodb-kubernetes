package v1_test

import (
	"os"
	"testing"

	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMain boots one envtest control plane shared by all tests in this package
// (see test/envtest/env). Future envtest-based tests in this package should use
// env.Shared(t) instead of starting their own environment.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("operator.mongodb.com_memberclusters.yaml", "operator.mongodb.com_operatorconfigs.yaml")))
}
