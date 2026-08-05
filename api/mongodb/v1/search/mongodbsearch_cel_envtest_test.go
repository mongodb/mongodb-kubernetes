package search_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMain boots one envtest control plane shared by all tests in this package
// (see test/envtest/env). Future envtest-based tests in this package should use
// env.Shared(t) instead of starting their own environment.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbsearch.yaml")))
}

// TestMongoDBSearchCELValidation proves that the CEL validation rules defined on
// the MongoDBSearch CRD are enforced by a real Kubernetes API server (booted
// locally via envtest). It covers both create-time rules and the oldSelf-based
// transition rule, neither of which can be exercised by plain Go unit tests.
func TestMongoDBSearchCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newSearch := func(name string, clusters ...searchv1.ClusterSpec) *searchv1.MongoDBSearch {
		return &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       searchv1.MongoDBSearchSpec{Clusters: clusters},
		}
	}

	tests := []struct {
		name string
		// create is the spec.clusters value submitted on create.
		create []searchv1.ClusterSpec
		// update, when non-nil, replaces spec.clusters in an update after a
		// successful create; the expectation then applies to that update
		// (for oldSelf-based transition rules).
		update []searchv1.ClusterSpec
		// errorContains is the expected CEL validation message;
		// empty means the operation must succeed.
		errorContains string
	}{
		{
			name:   "valid minimal spec is accepted",
			create: []searchv1.ClusterSpec{{}},
		},
		{
			name: "loadBalancer must set exactly one of managed or unmanaged",
			create: []searchv1.ClusterSpec{{
				LoadBalancer: &searchv1.LoadBalancerConfig{
					Managed:   &searchv1.ManagedLBConfig{},
					Unmanaged: &searchv1.UnmanagedLBConfig{},
				},
			}},
			errorContains: "exactly one of managed or unmanaged must be set",
		},
		{
			name: "cluster names are required when more than one cluster is specified",
			create: []searchv1.ClusterSpec{
				{Index: ptr.To(int32(0))},
				{Index: ptr.To(int32(1))},
			},
			errorContains: "clusters[].name must be set and unique when more than one cluster is specified",
		},
		{
			name: "cluster index must be unique",
			create: []searchv1.ClusterSpec{
				{Name: "cluster-a", Index: ptr.To(int32(0))},
				{Name: "cluster-b", Index: ptr.To(int32(0))},
			},
			errorContains: "clusters[].index must be unique when set",
		},
		{
			name:          "cluster name is immutable for an existing index",
			create:        []searchv1.ClusterSpec{{Name: "cluster-a", Index: ptr.To(int32(0))}},
			update:        []searchv1.ClusterSpec{{Name: "cluster-b", Index: ptr.To(int32(0))}},
			errorContains: "clusters[].name is immutable for an existing cluster index",
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newSearch(fmt.Sprintf("case-%d", i), tc.create...)
			err := k8sClient.Create(ctx, search)

			if tc.update != nil {
				require.NoError(t, err)
				search.Spec.Clusters = tc.update
				err = k8sClient.Update(ctx, search)
			}

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
