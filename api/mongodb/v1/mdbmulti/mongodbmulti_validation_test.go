package mdbmulti

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

var (
	ctx       = context.Background()
	validator = &MongoDBMultiClusterValidator{}
)

func TestUniqueClusterNames(t *testing.T) {
	ctx := context.Background()
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{
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
	validator := &MongoDBMultiClusterValidator{}
	_, err := validator.ValidateCreate(ctx, mrs)
	assert.ErrorContains(t, err, "Multiple clusters with the same name (abc) are not allowed")
}

func TestUniqueExternalDomains(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName:                 "1",
			Members:                     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("test")},
		},
		{
			ClusterName:                 "2",
			Members:                     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("test")},
		},
		{
			ClusterName:                 "3",
			Members:                     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("test")},
		},
	}

	_, err := validator.ValidateCreate(ctx, mrs)
	assert.ErrorContains(t, err, "Multiple member clusters with the same externalDomain (test) are not allowed")
}

func TestAllExternalDomainsSet(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName:                 "1",
			Members:                     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("test")},
		},
		{
			ClusterName:                 "2",
			Members:                     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: nil},
		},
		{
			ClusterName:                 "3",
			Members:                     1,
			ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("test")},
		},
	}

	_, err := validator.ValidateCreate(ctx, mrs)
	assert.ErrorContains(t, err, "The externalDomain is not set for member cluster: 2")
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
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: "foo",
		},
	}

	_, err := validator.ValidateCreate(ctx, mrs)
	assert.ErrorContains(t, err, "TLS must be enabled in order to use replica set horizons")
}

func TestSpecProjectOnlyOneValue(t *testing.T) {
	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{
		ConfigMapRef: mdbv1.ConfigMapRef{Name: "cloud-manager"},
	}
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{{
		ClusterName: "foo",
	}}

	_, err := validator.ValidateCreate(ctx, mrs)
	assert.NoError(t, err)
}
