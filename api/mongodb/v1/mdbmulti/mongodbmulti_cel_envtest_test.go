package mdbmulti_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mcdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbmulticluster.yaml")))
}

func newMongoDBMultiCluster(t *testing.T, role string) *mcdbmulti.MongoDBMultiCluster {
	t.Helper()
	mrs := mcdbmulti.DefaultMultiReplicaSetBuilder().SetRole(role).Build()
	mrs.Name = ""
	mrs.GenerateName = "test-cel-"
	mrs.Namespace = "default"
	mrs.Spec.Security.Authentication = nil
	return mrs
}

func setClusterSpecList(mrs *mcdbmulti.MongoDBMultiCluster, members ...int) {
	mrs.Spec.ClusterSpecList = make(mdbv1.ClusterSpecList, 0, len(members))
	for i, memberCount := range members {
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdbv1.ClusterSpecItem{
			ClusterName: fmt.Sprintf("cluster-%d", i+1),
			Members:     memberCount,
		})
	}
}

func newMongoDBMultiClusterObject(t *testing.T, role string) *unstructured.Unstructured {
	t.Helper()
	mrs := newMongoDBMultiCluster(t, role)
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
	require.NoError(t, err)
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
	return u
}

func TestMongoDBMultiClusterCELValidation_AppDBRole(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name              string
		setup             func(*testing.T) *unstructured.Unstructured
		updateAfterCreate bool
		errorContains     string
	}{
		{
			name: "empty role is accepted",
			setup: func(t *testing.T) *unstructured.Unstructured {
				return newMongoDBMultiClusterObject(t, "")
			},
		},
		{
			name: "role AppDB is accepted",
			setup: func(t *testing.T) *unstructured.Unstructured {
				mrs := newMongoDBMultiCluster(t, mdbv1.RoleAppDB)
				setClusterSpecList(mrs, 1, 1, 1)
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: obj}
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
				return u
			},
		},
		{
			name:          "role Bogus is rejected by DbCommonSpec validation",
			setup:         func(t *testing.T) *unstructured.Unstructured { return newMongoDBMultiClusterObject(t, "Bogus") },
			errorContains: "spec.role must be 'AppDB' when set",
		},
		{
			name: "role AppDB with sharded topology is rejected",
			setup: func(t *testing.T) *unstructured.Unstructured {
				mrs := newMongoDBMultiCluster(t, mdbv1.RoleAppDB)
				mrs.Spec.ResourceType = mdbv1.ShardedCluster
				setClusterSpecList(mrs, 1, 1, 1)
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: obj}
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
				return u
			},
			errorContains: "spec.resourceType must be ReplicaSet when spec.role is AppDB",
		},
		{
			name: "role AppDB with mixed auth modes is rejected",
			setup: func(t *testing.T) *unstructured.Unstructured {
				mrs := newMongoDBMultiCluster(t, mdbv1.RoleAppDB)
				mrs.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM, util.X509}, IgnoreUnknownUsers: true}
				setClusterSpecList(mrs, 1, 1, 1)
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: obj}
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
				return u
			},
			errorContains: "spec.security.authentication must be enabled with modes [SCRAM] only when spec.role is AppDB, or omitted entirely",
		},
		{
			name: "role AppDB with ignoreUnknownUsers false is rejected",
			setup: func(t *testing.T) *unstructured.Unstructured {
				mrs := newMongoDBMultiCluster(t, mdbv1.RoleAppDB)
				mrs.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM}, IgnoreUnknownUsers: false}
				setClusterSpecList(mrs, 1, 1, 1)
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: obj}
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
				return u
			},
			errorContains: "spec.security.authentication.ignoreUnknownUsers must be true when spec.role is AppDB and authentication is set",
		},
		{
			name: "role AppDB with too few members is rejected",
			setup: func(t *testing.T) *unstructured.Unstructured {
				mrs := newMongoDBMultiCluster(t, mdbv1.RoleAppDB)
				setClusterSpecList(mrs, 1, 1)
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: obj}
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
				return u
			},
			errorContains: "the total number of members across spec.clusterSpecList must be >= 3 when spec.role is AppDB",
		},
		{
			name: "role AppDB is immutable on update",
			setup: func(t *testing.T) *unstructured.Unstructured {
				mrs := newMongoDBMultiCluster(t, mdbv1.RoleAppDB)
				setClusterSpecList(mrs, 1, 1, 1)
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mrs)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: obj}
				u.SetGroupVersionKind(schema.GroupVersionKind{Group: "mongodb.com", Version: "v1", Kind: "MongoDBMultiCluster"})
				return u
			},
			updateAfterCreate: true,
			errorContains:     "spec.role is immutable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mrs := tc.setup(t)
			err := k8sClient.Create(ctx, mrs)

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			if tc.updateAfterCreate {
				require.NoError(t, err)
				spec := mrs.Object["spec"].(map[string]any)
				spec["role"] = ""
				err = k8sClient.Update(ctx, mrs)
			} else {
				require.Error(t, err)
			}

			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
