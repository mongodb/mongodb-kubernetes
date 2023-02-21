package mdbmulti

import (
	"k8s.io/utils/pointer"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/stretchr/testify/assert"
)

func TestUniqueClusterNames(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = []ClusterSpecItem{
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
	assert.Equal(t, "Multiple clusters with the same name (abc) are not allowed", err.Error())
}

func TestUniqueExternalDomains(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}
	mrs.Spec.ClusterSpecList = []ClusterSpecItem{
		{
			ClusterName:                 "1",
			Members:                     1,
			ExternalAccessConfiguration: mdbv1.ExternalAccessConfiguration{ExternalDomain: pointer.String("test")},
		},
		{
			ClusterName:                 "2",
			Members:                     1,
			ExternalAccessConfiguration: mdbv1.ExternalAccessConfiguration{ExternalDomain: pointer.String("test")},
		},
		{
			ClusterName:                 "3",
			Members:                     1,
			ExternalAccessConfiguration: mdbv1.ExternalAccessConfiguration{ExternalDomain: pointer.String("test")},
		},
	}

	err := mrs.ValidateCreate()
	assert.Equal(t, "Multiple externalDomains with the same name (test) are not allowed", err.Error())
}

func TestMongoDBMultiValidattionHorzonsWithoutTLS(t *testing.T) {
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12342"},
		{"my-horizon": "my-db.com:12346"},
	}

	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.Connectivity = &mdbv1.MongoDBConnectivity{
		ReplicaSetHorizons: replicaSetHorizons,
	}

	err := mrs.ValidateCreate()
	assert.Equal(t, "TLS must be enabled in order to use replica set horizons", err.Error())
}

func TestSpecProjectOnlyOneValue(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{
		ConfigMapRef: mdbv1.ConfigMapRef{Name: "cloud-manager"},
	}
	err := mrs.ValidateCreate()
	assert.NoError(t, err)
}
