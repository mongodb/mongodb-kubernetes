package operator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)


// TestSpecIndexProvider covers the 5 control-flow branches of the simulated-MC
// path: legacy passthrough, sharded-rejected, missing-clusterIndex-rejected,
// no-match-skip, and matched-projection. Pure function, no client needed.
func TestSpecIndexProvider_Resolve(t *testing.T) {
	idx := func(v int32) *int32 { return &v }
	withClusters := func(spec searchv1.MongoDBSearchSpec, cs ...searchv1.ClusterSpec) searchv1.MongoDBSearchSpec {
		if len(cs) == 0 {
			return spec
		}
		spec.Clusters = &cs
		return spec
	}
	rsSource := searchv1.MongoDBSearchSpec{
		Source: &searchv1.MongoDBSource{ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{HostAndPorts: []string{"mdb-0:27017"}}},
	}
	shardedSource := searchv1.MongoDBSearchSpec{
		Source: &searchv1.MongoDBSource{ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			ShardedCluster: &searchv1.ExternalShardedClusterConfig{},
		}},
	}

	tests := []struct {
		name              string
		operatorCluster   string
		spec              searchv1.MongoDBSearchSpec
		wantErrContains   string
		wantSkip          bool
		wantMapping       map[string]int
		wantSpecClustLen  int    // -1 = don't check
		wantSpecClustOnly string // expected single ClusterName after projection ("" = don't check)
	}{
		{
			name:             "legacy passthrough when spec.clusters is empty",
			operatorCluster:  "us-east",
			spec:             rsSource,
			wantMapping:      map[string]int{},
			wantSpecClustLen: -1,
		},
		{
			name:             "matched entry projects to 1-element slice + synthesised mapping",
			operatorCluster:  "us-east",
			spec:             withClusters(rsSource, searchv1.ClusterSpec{ClusterName: "us-east", ClusterIndex: idx(0)}, searchv1.ClusterSpec{ClusterName: "us-west", ClusterIndex: idx(1)}),
			wantMapping:      map[string]int{"us-east": 0},
			wantSpecClustLen: 1,
			wantSpecClustOnly: "us-east",
		},
		{
			name:             "matched entry honours non-zero ClusterIndex",
			operatorCluster:  "eu-central",
			spec:             withClusters(rsSource, searchv1.ClusterSpec{ClusterName: "us-east", ClusterIndex: idx(0)}, searchv1.ClusterSpec{ClusterName: "eu-central", ClusterIndex: idx(7)}),
			wantMapping:      map[string]int{"eu-central": 7},
			wantSpecClustLen: 1,
			wantSpecClustOnly: "eu-central",
		},
		{
			name:             "no matching entry returns Skip without mutating spec",
			operatorCluster:  "ap-south",
			spec:             withClusters(rsSource, searchv1.ClusterSpec{ClusterName: "us-east", ClusterIndex: idx(0)}, searchv1.ClusterSpec{ClusterName: "us-west", ClusterIndex: idx(1)}),
			wantSkip:         true,
			wantSpecClustLen: 2, // unmutated
		},
		{
			name:            "missing clusterIndex on any entry returns error",
			operatorCluster: "us-east",
			spec:            withClusters(rsSource, searchv1.ClusterSpec{ClusterName: "us-east", ClusterIndex: idx(0)}, searchv1.ClusterSpec{ClusterName: "us-west" /* no idx */}),
			wantErrContains: "clusterIndex to be set",
		},
		{
			name:            "sharded external source is rejected",
			operatorCluster: "us-east",
			spec:            withClusters(shardedSource, searchv1.ClusterSpec{ClusterName: "us-east", ClusterIndex: idx(0)}),
			wantErrContains: "replica-set source only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{Name: "mysearch", Namespace: mock.TestNamespace},
				Spec:       tt.spec,
			}
			p := NewSpecIndexProvider(tt.operatorCluster)
			got, err := p.Resolve(context.Background(), search, zap.S())

			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSkip, got.Skip, "Skip")
			if !tt.wantSkip {
				assert.Equal(t, tt.wantMapping, got.Mapping)
			}
			if tt.wantSpecClustLen >= 0 {
				if tt.wantSpecClustLen == 0 || search.Spec.Clusters == nil {
					if search.Spec.Clusters != nil {
						assert.Empty(t, *search.Spec.Clusters)
					}
				} else {
					require.NotNil(t, search.Spec.Clusters)
					assert.Len(t, *search.Spec.Clusters, tt.wantSpecClustLen)
					if tt.wantSpecClustOnly != "" {
						assert.Equal(t, tt.wantSpecClustOnly, (*search.Spec.Clusters)[0].ClusterName)
					}
				}
			}
		})
	}
}

// TestStateCMProvider covers fresh-CR initialisation and write-back on mapping
// change. The full re-add/no-op/operator-restart matrix is already covered by
// TestMongoDBSearchControllerReconcile_StateConfigMap in the controller test;
// this just exercises the provider's wrapping of loadOrInitSearchState +
// AssignClusterIndices.
func TestStateCMProvider_Resolve(t *testing.T) {
	ctx := context.Background()
	scheme := envoyTestScheme(t)

	t.Run("fresh CR creates state CM + assigns indices monotonically", func(t *testing.T) {
		search := &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "mysearch", Namespace: mock.TestNamespace, UID: "test-uid"},
			Spec: searchv1.MongoDBSearchSpec{
				Clusters: &[]searchv1.ClusterSpec{{ClusterName: "us-east"}, {ClusterName: "us-west"}},
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(search).Build()
		p := NewStateCMProvider(kubernetesClient.NewClient(fakeClient))

		got, err := p.Resolve(ctx, search, zap.S())
		require.NoError(t, err)
		assert.False(t, got.Skip)
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, got.Mapping)

		// State CM must exist on the central client after the write.
		stateCM := &corev1.ConfigMap{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM))
		var state SearchDeploymentState
		require.NoError(t, json.Unmarshal([]byte(stateCM.Data["state"]), &state))
		assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, state.ClusterMapping)
	})

	t.Run("stable mapping does NOT trigger a write-back", func(t *testing.T) {
		search := &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "mysearch", Namespace: mock.TestNamespace, UID: "test-uid"},
			Spec: searchv1.MongoDBSearchSpec{
				Clusters: &[]searchv1.ClusterSpec{{ClusterName: "us-east"}, {ClusterName: "us-west"}},
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(search).Build()
		p := NewStateCMProvider(kubernetesClient.NewClient(fakeClient))

		_, err := p.Resolve(ctx, search, zap.S())
		require.NoError(t, err)
		stateCM1 := &corev1.ConfigMap{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM1))
		rvBefore := stateCM1.ResourceVersion

		// Second resolve with unchanged spec — must not rewrite the CM.
		_, err = p.Resolve(ctx, search, zap.S())
		require.NoError(t, err)
		stateCM2 := &corev1.ConfigMap{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mysearch-state", Namespace: mock.TestNamespace}, stateCM2))
		assert.Equal(t, rvBefore, stateCM2.ResourceVersion, "state CM must not be rewritten when mapping is unchanged")
	})
}
