package mdbmulti

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
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
	file := createTestKubeConfigAndSetEnv(t)
	defer os.Remove(file.Name())

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

// appDBRoleReadyMultiReplicaSet builds a MongoDBMultiCluster with spec.role: AppDB and every
// AppDB requirement satisfied (SCRAM enabled, ignoreUnknownUsers true, 3 members summed across
// ClusterSpecList, version 4.0.0).
func appDBRoleReadyMultiReplicaSet() *MongoDBMultiCluster {
	mrs := DefaultMultiReplicaSetBuilder().
		SetVersion("4.0.0").
		SetClusterSpecList([]string{"cluster-1", "cluster-2"}).
		Build()
	mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{
		{ClusterName: "cluster-1", Members: 2},
		{ClusterName: "cluster-2", Members: 1},
	}
	mrs.Spec.Role = mdbv1.RoleAppDB
	mrs.Spec.Security.Authentication.Enabled = true
	mrs.Spec.Security.Authentication.Modes = []mdbv1.AuthMode{mdbv1.AuthMode(util.SCRAM)}
	mrs.Spec.Security.Authentication.IgnoreUnknownUsers = true
	return mrs
}

func TestMongoDBMulti_AppDBRoleValidation(t *testing.T) {
	tests := []struct {
		name                 string
		mutate               func(mrs *MongoDBMultiCluster)
		expectedErrorMessage string
	}{
		{
			name: "role not set - other invalid fields are ignored",
			mutate: func(mrs *MongoDBMultiCluster) {
				mrs.Spec.Role = ""
				mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{{ClusterName: "cluster-1", Members: 1}}
				mrs.Spec.Security.Authentication = nil
			},
		},
		{
			name: "role AppDB with fewer than 3 total members across ClusterSpecList",
			mutate: func(mrs *MongoDBMultiCluster) {
				mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{
					{ClusterName: "cluster-1", Members: 1},
					{ClusterName: "cluster-2", Members: 1},
				}
			},
			expectedErrorMessage: "spec.clusterSpecList members must sum to >= 3 when spec.role is AppDB",
		},
		{
			name:   "role AppDB with 3 total members across ClusterSpecList - everything satisfied",
			mutate: func(mrs *MongoDBMultiCluster) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mrs := appDBRoleReadyMultiReplicaSet()
			tt.mutate(mrs)

			err := mrs.ProcessValidationsOnReconcile(nil)

			if tt.expectedErrorMessage != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tt.expectedErrorMessage)
			} else {
				require.NoError(t, err)
			}
		})
	}
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

func TestMongoDBMulti_RoleImmutable(t *testing.T) {
	// roleImmutable message; computed here to keep the expectation in one place
	const immutableError = "spec.role is immutable: it cannot be added, removed, or changed after creation; to stop using a resource as AppDB, perform a reverse migration (delete the resource)"

	buildMrs := func(role string) *MongoDBMultiCluster {
		if role == mdbv1.RoleAppDB {
			return appDBRoleReadyMultiReplicaSet()
		}
		return DefaultMultiReplicaSetBuilder().SetClusterSpecList([]string{"cluster-1"}).Build()
	}

	tests := []struct {
		name          string
		oldRole       string
		newRole       string
		expectedError string
	}{
		{name: "removing role AppDB is rejected", oldRole: mdbv1.RoleAppDB, newRole: "", expectedError: immutableError},
		{name: "adding role AppDB is rejected", oldRole: "", newRole: mdbv1.RoleAppDB, expectedError: immutableError},
		{name: "unchanged role AppDB is allowed", oldRole: mdbv1.RoleAppDB, newRole: mdbv1.RoleAppDB},
		{name: "unchanged empty role is allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMrs := buildMrs(tt.oldRole)
			newMrs := buildMrs(tt.newRole)

			_, err := validator.ValidateUpdate(ctx, oldMrs, newMrs)
			if tt.expectedError == "" {
				assert.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.expectedError)
			}
		})
	}
}
