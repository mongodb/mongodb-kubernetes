package mdbmulti

import (
	"fmt"
	"os"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/stretchr/testify/assert"
)

func TestUniqueClusterNames(t *testing.T) {
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

	_, err := mrs.ValidateCreate()
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

	_, err := mrs.ValidateCreate()
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

	_, err := mrs.ValidateCreate()
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

	_, err := mrs.ValidateCreate()
	assert.ErrorContains(t, err, "TLS must be enabled in order to use replica set horizons")
}

func TestSpecProjectOnlyOneValue(t *testing.T) {
	file := createTestKubeConfigAndSetEnv(t)
	defer os.Remove(file.Name())

	mrs := DefaultMultiReplicaSetBuilder().Build()
	mrs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{
		ConfigMapRef: mdbv1.ConfigMapRef{Name: "cloud-manager"},
	}
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{{
		ClusterName: "foo",
	}}

	_, err := mrs.ValidateCreate()
	assert.NoError(t, err)
}

func createTestKubeConfigAndSetEnv(t *testing.T) *os.File {
	//lint:ignore S1039 I avoid to modify this string to not ruin the format
	//nolint
	testKubeConfig := fmt.Sprintf(
		`
apiVersion: v1
contexts:
- context:
    cluster: foo
    namespace: a-1661872869-pq35wlt3zzz
    user: foo
  name: foo
kind: Config
users:
- name: foo
  user:
    token: eyJhbGciOi
`)

	file, err := os.CreateTemp("", "kubeconfig")
	assert.NoError(t, err)

	_, err = file.WriteString(testKubeConfig)
	assert.NoError(t, err)

	t.Setenv(multicluster.KubeConfigPathEnv, file.Name())

	return file
}
