package mdbmulti

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUniqueClusterNames(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList.ClusterSpecs = []ClusterSpecItem{
		{
			ClusterName: "abc",
			Members:     2,
		},
		{
			ClusterName: "def",
			Members:     1,
		},
		{
			ClusterName: "abc",
			Members:     1,
		},
	}

	err := mrs.ValidateCreate()
	assert.Errorf(t, err, "Multiple clusters with the same name(abc) is not allowed")
}
