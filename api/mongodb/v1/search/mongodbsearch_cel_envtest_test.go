package search_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMongoDBSearchCELValidation proves that the CEL validation rules defined on
// the MongoDBSearch CRD are enforced by a real Kubernetes API server (booted
// locally via envtest). It covers both create-time rules and the oldSelf-based
// transition rule, neither of which can be exercised by plain Go unit tests.
func TestMongoDBSearchCELValidation(t *testing.T) {
	ctx := context.Background()
	testEnv := env.Start(t, env.WithCRDs("mongodb.com_mongodbsearch.yaml"))
	k8sClient := testEnv.Client

	newSearch := func(name string, clusters ...searchv1.ClusterSpec) *searchv1.MongoDBSearch {
		return &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       searchv1.MongoDBSearchSpec{Clusters: clusters},
		}
	}

	t.Run("valid minimal spec is accepted", func(t *testing.T) {
		search := newSearch("valid", searchv1.ClusterSpec{})
		require.NoError(t, k8sClient.Create(ctx, search))
		require.NoError(t, k8sClient.Delete(ctx, search))
	})

	rejectionTests := []struct {
		name          string
		clusters      []searchv1.ClusterSpec
		errorContains string
	}{
		{
			name: "loadBalancer must set exactly one of managed or unmanaged",
			clusters: []searchv1.ClusterSpec{{
				LoadBalancer: &searchv1.LoadBalancerConfig{
					Managed:   &searchv1.ManagedLBConfig{},
					Unmanaged: &searchv1.UnmanagedLBConfig{},
				},
			}},
			errorContains: "exactly one of managed or unmanaged must be set",
		},
		{
			name: "cluster names are required when more than one cluster is specified",
			clusters: []searchv1.ClusterSpec{
				{Index: ptr.To(int32(0))},
				{Index: ptr.To(int32(1))},
			},
			errorContains: "clusters[].name must be set and unique when more than one cluster is specified",
		},
		{
			name: "cluster index must be unique",
			clusters: []searchv1.ClusterSpec{
				{Name: "cluster-a", Index: ptr.To(int32(0))},
				{Name: "cluster-b", Index: ptr.To(int32(0))},
			},
			errorContains: "clusters[].index must be unique when set",
		},
	}
	for _, tc := range rejectionTests {
		t.Run(tc.name, func(t *testing.T) {
			err := k8sClient.Create(ctx, newSearch("invalid", tc.clusters...))
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}

	t.Run("cluster name is immutable for an existing index", func(t *testing.T) {
		search := newSearch("immutable-name", searchv1.ClusterSpec{Name: "cluster-a", Index: ptr.To(int32(0))})
		require.NoError(t, k8sClient.Create(ctx, search))

		search.Spec.Clusters[0].Name = "cluster-b"
		err := k8sClient.Update(ctx, search)
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
		assert.Contains(t, err.Error(), "clusters[].name is immutable for an existing cluster index")
	})
}
