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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
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

// The routing-ready switch persists through RV-checked read-modify-writes on the
// state ConfigMap: creation with owner identity, monotonic appends, no-op
// rewrites, pruning, and 409 Conflict on a stale base instead of a silent lost update.
func TestRoutingSwitch_StateCMWrites(t *testing.T) {
	ctx := context.Background()
	search := newTestMongoDBSearch("mysearch", mock.TestNamespace)
	search.UID = "search-uid"
	stateCMName := types.NamespacedName{Name: "mysearch-search-state", Namespace: mock.TestNamespace}

	newHelper := func(c client.Client) *MongoDBSearchReconcileHelper {
		return NewMongoDBSearchReconcileHelper(kubernetesClient.NewClient(c), search, nil, OperatorSearchConfig{}, nil, nil)
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
		assert.Equal(t, string(search.UID), cm.Labels[khandler.MongoDBSearchOwnerUIDLabel])
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

	t.Run("missing uid marker adopts legacy state and stamps current uid", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		legacyState, err := json.Marshal(SearchDeploymentState{RoutingReadyMongotGroups: []string{"legacy-shard"}})
		require.NoError(t, err)
		require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stateCMName.Name,
				Namespace: stateCMName.Namespace,
				Labels: map[string]string{
					khandler.MongoDBSearchOwnerNameLabel:      search.Name,
					khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
				},
			},
			Data: map[string]string{searchStateKey: string(legacyState)},
		}))

		helper := newHelper(c)
		require.NoError(t, helper.markRoutingReady(ctx, "sh-0"))
		assert.Equal(t, []string{"legacy-shard", "sh-0"}, readState(t, c).RoutingReadyMongotGroups)

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		assert.Equal(t, string(search.UID), cm.Labels[khandler.MongoDBSearchOwnerUIDLabel])
		require.Len(t, cm.OwnerReferences, 1)
		assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
	})

	t.Run("no-op mutation adopts legacy identity without changing state", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		legacyState, err := json.Marshal(SearchDeploymentState{RoutingReadyMongotGroups: []string{"legacy-shard"}})
		require.NoError(t, err)
		require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stateCMName.Name,
				Namespace: stateCMName.Namespace,
				Labels: map[string]string{
					khandler.MongoDBSearchOwnerNameLabel:      search.Name,
					khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
				},
				OwnerReferences: kube.BaseOwnerReference(search),
			},
			Data: map[string]string{searchStateKey: string(legacyState)},
		}))

		state, err := MutateSearchState(ctx, kubernetesClient.NewClient(c), search, func(*SearchDeploymentState) bool {
			return false
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"legacy-shard"}, state.RoutingReadyMongotGroups)

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		require.Len(t, cm.OwnerReferences, 1)
		assert.Equal(t, search.UID, cm.OwnerReferences[0].UID)
		assert.Equal(t, string(search.UID), cm.Labels[khandler.MongoDBSearchOwnerUIDLabel])
		assert.Equal(t, string(legacyState), cm.Data[searchStateKey])
	})

	t.Run("no-op mutation does not create missing state", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		state, err := MutateSearchState(ctx, kubernetesClient.NewClient(c), search, func(*SearchDeploymentState) bool {
			return false
		})
		require.NoError(t, err)
		assert.Empty(t, state.RoutingReadyMongotGroups)

		cm := &corev1.ConfigMap{}
		err = c.Get(ctx, stateCMName, cm)
		assert.True(t, apierrors.IsNotFound(err))
	})

	t.Run("uid mismatch resets stale state and preserves owner references", func(t *testing.T) {
		c := mock.NewEmptyFakeClientBuilder().Build()
		staleState, err := json.Marshal(SearchDeploymentState{RoutingReadyMongotGroups: []string{"stale-shard"}})
		require.NoError(t, err)
		require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stateCMName.Name,
				Namespace: stateCMName.Namespace,
				Labels: map[string]string{
					khandler.MongoDBSearchOwnerNameLabel:      search.Name,
					khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
					khandler.MongoDBSearchOwnerUIDLabel:       "old-search-uid",
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion:         "mongodb.com/v1",
					Kind:               "MongoDBSearch",
					Name:               search.Name,
					UID:                types.UID("old-search-uid"),
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				}},
			},
			Data: map[string]string{searchStateKey: string(staleState)},
		}))

		helper := newHelper(c)
		require.NoError(t, helper.markRoutingReady(ctx, "sh-0"))
		assert.Equal(t, []string{"sh-0"}, readState(t, c).RoutingReadyMongotGroups)
		assert.False(t, switchedOn(helper, "stale-shard"))

		cm := &corev1.ConfigMap{}
		require.NoError(t, c.Get(ctx, stateCMName, cm))
		require.Len(t, cm.OwnerReferences, 2)
		assert.ElementsMatch(t, []types.UID{"old-search-uid", search.UID}, []types.UID{
			cm.OwnerReferences[0].UID,
			cm.OwnerReferences[1].UID,
		})
		assert.True(t, *cm.OwnerReferences[0].Controller, "existing controller owner reference must be preserved")
		assert.Nil(t, cm.OwnerReferences[1].Controller, "current UID adoption reference must not replace the existing controller")
		assert.Equal(t, string(search.UID), cm.Labels[khandler.MongoDBSearchOwnerUIDLabel])
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

func TestReadSearchState_UIDGuardBehavior(t *testing.T) {
	ctx := context.Background()
	search := newTestMongoDBSearch("mysearch", mock.TestNamespace)
	search.UID = "new-search-uid"

	storedStateRaw, err := json.Marshal(SearchDeploymentState{RoutingReadyMongotGroups: []string{"stored-shard"}})
	require.NoError(t, err)

	tests := []struct {
		name           string
		recordedUID    *string
		controllerUID  *types.UID
		expectedGroups []string
	}{
		{name: "missing uid marker without controller adopts legacy state", expectedGroups: []string{"stored-shard"}},
		{name: "missing uid marker with current controller adopts legacy state", controllerUID: ptr.To(search.UID), expectedGroups: []string{"stored-shard"}},
		{name: "missing uid marker with stale controller returns fresh state", controllerUID: ptr.To(types.UID("old-search-uid"))},
		{name: "matching uid keeps stored state despite stale controller", recordedUID: ptr.To(string(search.UID)), controllerUID: ptr.To(types.UID("old-search-uid")), expectedGroups: []string{"stored-shard"}},
		{name: "uid mismatch returns fresh state despite current controller", recordedUID: ptr.To("old-search-uid"), controllerUID: ptr.To(search.UID)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			labels := map[string]string{
				khandler.MongoDBSearchOwnerNameLabel:      search.Name,
				khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
			}
			if tc.recordedUID != nil {
				labels[khandler.MongoDBSearchOwnerUIDLabel] = *tc.recordedUID
			}
			var ownerReferences []metav1.OwnerReference
			if tc.controllerUID != nil {
				ownerReferences = kube.BaseOwnerReference(search)
				ownerReferences[0].UID = *tc.controllerUID
			}
			stateCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            SearchStateCMName(search),
					Namespace:       search.Namespace,
					Labels:          labels,
					OwnerReferences: ownerReferences,
				},
				Data: map[string]string{searchStateKey: string(storedStateRaw)},
			}

			c := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(stateCM).Build())
			state, err := ReadSearchState(ctx, c, search)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedGroups, state.RoutingReadyMongotGroups)

			cm := &corev1.ConfigMap{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: stateCM.Name, Namespace: stateCM.Namespace}, cm))
			recordedUID, hasUIDLabel := cm.Labels[khandler.MongoDBSearchOwnerUIDLabel]
			assert.Equal(t, tc.recordedUID != nil, hasUIDLabel, "ReadSearchState must stay read-only")
			if tc.recordedUID != nil {
				assert.Equal(t, *tc.recordedUID, recordedUID, "ReadSearchState must stay read-only")
			}
			controller := metav1.GetControllerOf(cm)
			if tc.controllerUID == nil {
				assert.Nil(t, controller)
			} else {
				require.NotNil(t, controller)
				assert.Equal(t, *tc.controllerUID, controller.UID, "ReadSearchState must stay read-only")
			}
		})
	}
}

func TestMutateSearchState_UIDGuardBehavior(t *testing.T) {
	ctx := context.Background()
	search := newTestMongoDBSearch("mysearch", mock.TestNamespace)
	search.UID = "new-search-uid"

	storedStateRaw, err := json.Marshal(SearchDeploymentState{RoutingReadyMongotGroups: []string{"stored-shard"}})
	require.NoError(t, err)

	tests := []struct {
		name           string
		controllerUID  *types.UID
		expectedGroups []string
	}{
		{name: "missing uid marker without controller adopts legacy state", expectedGroups: []string{"stored-shard", "new-shard"}},
		{name: "missing uid marker with current controller adopts legacy state", controllerUID: ptr.To(search.UID), expectedGroups: []string{"stored-shard", "new-shard"}},
		{name: "missing uid marker with stale controller resets state", controllerUID: ptr.To(types.UID("old-search-uid")), expectedGroups: []string{"new-shard"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ownerReferences []metav1.OwnerReference
			if tc.controllerUID != nil {
				ownerReferences = kube.BaseOwnerReference(search)
				ownerReferences[0].UID = *tc.controllerUID
			}
			stateCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            SearchStateCMName(search),
					Namespace:       search.Namespace,
					Labels:          search.GetOwnerLabels(),
					OwnerReferences: ownerReferences,
				},
				Data: map[string]string{searchStateKey: string(storedStateRaw)},
			}
			c := kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(stateCM).Build())

			state, err := MutateSearchState(ctx, c, search, func(state *SearchDeploymentState) bool {
				state.RoutingReadyMongotGroups = append(state.RoutingReadyMongotGroups, "new-shard")
				return true
			})
			require.NoError(t, err)
			assert.Equal(t, tc.expectedGroups, state.RoutingReadyMongotGroups)

			cm := &corev1.ConfigMap{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: stateCM.Name, Namespace: stateCM.Namespace}, cm))
			var storedState SearchDeploymentState
			require.NoError(t, decodeStateJSON(cm, &storedState))
			assert.Equal(t, tc.expectedGroups, storedState.RoutingReadyMongotGroups)
			assert.Equal(t, string(search.UID), cm.Labels[khandler.MongoDBSearchOwnerUIDLabel])
			assert.True(t, slices.ContainsFunc(cm.OwnerReferences, func(ref metav1.OwnerReference) bool {
				return ref.UID == search.UID
			}))
			controller := metav1.GetControllerOf(cm)
			if tc.controllerUID == nil {
				assert.Nil(t, controller)
			} else {
				require.NotNil(t, controller)
				assert.Equal(t, *tc.controllerUID, controller.UID, "existing controller reference must be preserved")
			}
		})
	}
}
