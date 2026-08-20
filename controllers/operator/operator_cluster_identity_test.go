package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestGetOperatorClusterName(t *testing.T) {
	// unset: no per-cluster identity, hub-and-spoke mode
	assert.Equal(t, "", GetOperatorClusterName())

	t.Setenv(util.OperatorClusterNameEnv, "cluster-1")
	assert.Equal(t, "cluster-1", GetOperatorClusterName())

	// explicitly empty behaves like unset
	t.Setenv(util.OperatorClusterNameEnv, "")
	assert.Equal(t, "", GetOperatorClusterName())
}
