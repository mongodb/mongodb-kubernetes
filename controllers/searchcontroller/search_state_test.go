package searchcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

func decodeStateJSON(cm *corev1.ConfigMap, dst interface{}) error {
	raw, ok := cm.Data[searchStateKey]
	if !ok {
		return fmt.Errorf("state key missing from ConfigMap %s", cm.Name)
	}
	return json.Unmarshal([]byte(raw), dst)
}

func buildSearchStateCM(t *testing.T, search *searchv1.MongoDBSearch, labels map[string]string, ownerRefs []metav1.OwnerReference, state SearchDeploymentState) *corev1.ConfigMap {
	t.Helper()
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            SearchStateCMName(search),
			Namespace:       search.Namespace,
			Labels:          labels,
			OwnerReferences: ownerRefs,
		},
		Data: map[string]string{searchStateKey: string(raw)},
	}
}

// The routing-ready switch persists through RV-checked read-modify-writes on the
// state ConfigMap: creation with owner identity, monotonic appends, legacy metadata
// repair, no-op rewrites, pruning, and 409 Conflict on a stale base instead of a
// silent lost update.
func TestRoutingSwitch_StateCMWrites(t *testing.T) {
	ctx := context.Background()
	search := newTestMongoDBSearch("mysearch", mock.TestNamespace)
	search.UID = "search-uid"
	stateCMName := types.NamespacedName{Name: "mysearch-search-state", Namespace: mock.TestNamespace}

	newHelper := func(c client.Client) *MongoDBSearchReconcileHelper {
		return NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(c), search, nil, OperatorSearchConfig{}, nil, nil, false)
	}
	switchedOn := func(h *MongoDBSearchReconcileHelper, shard string) bool {
		return slices.Contains(h.state.RoutingReadyMongotGroups, shard)
	}
	readState := func(t *testing.T, c client.Client) SearchDeploymentState {
		t.Helper()
		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		var st SearchDeploymentState
		require.NoError(t, decodeStateJSON(cm, &st))
		return st
	}

	t.Run("first mark creates the state CM with owner identity", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		helper := newHelper(c)
		require.NoError(t, helper.markRoutingReady(ctx, "sh-0"))
		assert.True(t, switchedOn(helper, "sh-0"))

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		assert.Equal(t, []string{"sh-0"}, readState(t, c).RoutingReadyMongotGroups)
		require.Len(t, cm.OwnerReferences, 1)
		assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
		for k, v := range searchOwnerLabels(search, "") {
			assert.Equal(t, v, cm.Labels[k], "owner label %s", k)
		}
	})

	t.Run("mark appends to an existing switch", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		_, err := MutateSearchState(ctx, kubernetesClient.NewClient(c), search, func(s *SearchDeploymentState) bool {
			s.RoutingReadyMongotGroups = []string{"sh-0"}
			return true
		})
		require.NoError(t, err)

		require.NoError(t, newHelper(c).markRoutingReady(ctx, "sh-1"))
		assert.Equal(t, []string{"sh-0", "sh-1"}, readState(t, c).RoutingReadyMongotGroups)
	})

	legacyState := SearchDeploymentState{RoutingReadyMongotGroups: []string{"legacy-shard"}}

	t.Run("mark repairs owner labels on a legacy state CM", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().
			WithObjects(buildSearchStateCM(t, search, search.GetOwnerLabels(), kube.BaseOwnerReference(search), legacyState)).
			Build()
		helper := newHelper(c)
		require.NoError(t, helper.markRoutingReady(ctx, "sh-0"))
		assert.Equal(t, []string{"legacy-shard", "sh-0"}, helper.state.RoutingReadyMongotGroups)

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		for k, v := range searchOwnerLabels(search, "") {
			assert.Equal(t, v, cm.Labels[k], "owner label %s", k)
		}
		require.Len(t, cm.OwnerReferences, 1, "current-CR owner reference must not be duplicated")
	})

	t.Run("no-op mutation adopts a state CM owned by a previous CR", func(t *testing.T) {
		staleOwnerRefs := kube.BaseOwnerReference(search)
		staleOwnerRefs[0].UID = "old-search-uid"
		c := mock.NewEmptyFakeClientBuilder().
			WithObjects(buildSearchStateCM(t, search, nil, staleOwnerRefs, legacyState)).
			Build()

		state, err := MutateSearchState(ctx, kubernetesClient.NewClient(c), search, func(*SearchDeploymentState) bool {
			return false
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"legacy-shard"}, state.RoutingReadyMongotGroups)
		assert.Equal(t, []string{"legacy-shard"}, readState(t, c).RoutingReadyMongotGroups)

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		for k, v := range searchOwnerLabels(search, "") {
			assert.Equal(t, v, cm.Labels[k], "owner label %s", k)
		}
		var uids, controllerUIDs []types.UID
		for _, ref := range cm.OwnerReferences {
			uids = append(uids, ref.UID)
			if ref.Controller != nil && *ref.Controller {
				controllerUIDs = append(controllerUIDs, ref.UID)
			}
		}
		assert.ElementsMatch(t, []types.UID{"old-search-uid", search.UID}, uids)
		assert.Equal(t, []types.UID{"old-search-uid"}, controllerUIDs, "adoption must not replace the existing controller owner reference")
	})

	t.Run("no-op mutation does not create missing state", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		state, err := MutateSearchState(ctx, kubernetesClient.NewClient(c), search, func(*SearchDeploymentState) bool {
			return false
		})
		require.NoError(t, err)
		assert.Empty(t, state.RoutingReadyMongotGroups)

		err = c.Get(ctx, stateCMName, &corev1.ConfigMap{})
		assert.True(t, apierrors.IsNotFound(err), "no-op mutation must not create the state CM")
	})

	t.Run("already-on switch is a no-op write", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		helper := newHelper(c)
		require.NoError(t, helper.markRoutingReady(ctx, "sh-0"))
		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		rv := cm.ResourceVersion

		require.NoError(t, helper.markRoutingReady(ctx, "sh-0"))
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		assert.Equal(t, rv, cm.ResourceVersion, "re-marking an already-on shard must not rewrite the CM")
	})

	t.Run("prune removes only shards that no longer exist", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		helper := newHelper(c)
		for _, shard := range []string{"sh-0", "sh-1", "sh-2"} {
			require.NoError(t, helper.markRoutingReady(ctx, shard))
		}

		require.NoError(t, helper.pruneRoutingReady(ctx, []string{"sh-0", "sh-2"}))
		assert.Equal(t, []string{"sh-0", "sh-2"}, readState(t, c).RoutingReadyMongotGroups)
		assert.False(t, switchedOn(helper, "sh-1"))

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		rv := cm.ResourceVersion
		require.NoError(t, helper.pruneRoutingReady(ctx, []string{"sh-0", "sh-2"}))
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		assert.Equal(t, rv, cm.ResourceVersion, "prune without changes must not rewrite the CM")
	})

	t.Run("stale base write conflicts instead of clobbering", func(t *testing.T) {
		base := mock.NewEmptyFakeClientBuilder().Build()
		require.NoError(t, newHelper(base).markRoutingReady(ctx, "sh-0"))

		// A concurrent writer lands between our Get and Update: the write must
		// surface 409 Conflict (the fake client enforces the carried RV), never
		// silently overwrite the concurrent change.
		raced := false
		c := interceptor.NewClient(base, interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if key.Name == stateCMName.Name && !raced {
					raced = true
					concurrent := obj.(*corev1.ConfigMap).DeepCopy()
					concurrent.Data["concurrent"] = "write"
					if err := cl.Update(ctx, concurrent); err != nil {
						return err
					}
				}
				return nil
			},
		})

		err := newHelper(c).markRoutingReady(ctx, "sh-1")
		require.Error(t, err)
		assert.True(t, apierrors.IsConflict(err), "stale base must surface 409 Conflict, got: %v", err)
	})
}
