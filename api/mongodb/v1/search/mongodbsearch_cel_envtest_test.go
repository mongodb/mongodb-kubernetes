package search_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMain boots one envtest control plane shared by all tests in this package
// (see test/envtest/env). Future envtest-based tests in this package should use
// env.Shared(t) instead of starting their own environment.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbsearch.yaml")))
}

func TestMongoDBSearchServiceMetadataPersistence(t *testing.T) {
	k8sClient := env.Shared(t).Client
	search := &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "service-metadata-", Namespace: "default"},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
				ShardedCluster: &searchv1.ExternalShardedClusterConfig{
					Router: searchv1.ExternalRouterConfig{Hosts: []string{"mongos.example:27017"}},
					Shards: []searchv1.ExternalShardConfig{{ShardName: "shard-0", Hosts: []string{"shard.example:27017"}}},
				},
			}},
			Clusters: []searchv1.ClusterSpec{{
				Service: &v1.ServiceConfiguration{MetadataWrapper: v1.ServiceMetadataWrapper{
					Labels:      map[string]string{"app.kubernetes.io/component": "search"},
					Annotations: map[string]string{"example.com/note": "cluster"},
				}},
				ShardOverrides: []searchv1.ShardOverride{{
					ShardNames: []string{"shard-0"},
					Service: &v1.ServiceConfiguration{MetadataWrapper: v1.ServiceMetadataWrapper{
						Labels:      map[string]string{"example.com/team": "shard"},
						Annotations: map[string]string{"example.com/note": "shard"},
					}},
				}},
			}},
		},
	}
	want := search.DeepCopy()
	require.NoError(t, k8sClient.Create(t.Context(), search))
	t.Cleanup(func() { require.NoError(t, k8sClient.Delete(context.Background(), search)) })
	var fetched searchv1.MongoDBSearch
	require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(search), &fetched))
	require.Len(t, fetched.Spec.Clusters, 1)
	require.Len(t, fetched.Spec.Clusters[0].ShardOverrides, 1)
	assert.Equal(t, want.Spec.Clusters[0].Service, fetched.Spec.Clusters[0].Service)
	assert.Equal(t, want.Spec.Clusters[0].ShardOverrides[0].Service, fetched.Spec.Clusters[0].ShardOverrides[0].Service)
}

func TestServiceMetadataRealAPI(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	k8sClient, err := client.New(env.Shared(t).Config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	for _, tc := range []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		valid       bool
	}{
		{"valid", map[string]string{"app.kubernetes.io/component": "search"}, map[string]string{"example.com/note": "any text allowed"}, true},
		{"invalid label", map[string]string{"team": "not a label value"}, nil, false},
		{"invalid annotation key", nil, map[string]string{"bad key": "value"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := service.Builder().SetNamespace("default").
				SetLabels(tc.labels).SetAnnotations(tc.annotations).
				AddPort(&corev1.ServicePort{Name: "mongot-grpc", Port: 27028}).Build()
			svc.GenerateName = "metadata-"
			err := k8sClient.Create(t.Context(), &svc)
			if !tc.valid {
				require.True(t, apierrors.IsInvalid(err), "expected API validation rejection: %v", err)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, k8sClient.Delete(context.Background(), &svc)) })
			var fetched corev1.Service
			require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(&svc), &fetched))
			assert.Equal(t, tc.labels, fetched.Labels)
			assert.Equal(t, tc.annotations, fetched.Annotations)
		})
	}
}

// TestMongoDBSearchCELValidation proves that the CEL validation rules defined on
// the MongoDBSearch CRD are enforced by a real Kubernetes API server (booted
// locally via envtest). It covers both create-time rules and the oldSelf-based
// transition rule, neither of which can be exercised by plain Go unit tests.
func TestMongoDBSearchCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newSearch := func(clusters ...searchv1.ClusterSpec) *searchv1.MongoDBSearch {
		return &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-", Namespace: "default"},
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
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newSearch(tc.create...)
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
