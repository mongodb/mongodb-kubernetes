package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSyncDependentWatchedResources(t *testing.T) {
	owner := types.NamespacedName{Name: "search", Namespace: "ns"}
	other := types.NamespacedName{Name: "other", Namespace: "ns"}
	shared := Object{ResourceType: Secret, Resource: types.NamespacedName{Name: "shared", Namespace: "ns"}}
	old := Object{ResourceType: ConfigMap, Resource: types.NamespacedName{Name: "old", Namespace: "ns"}}
	replacement := Object{ResourceType: Secret, Resource: types.NamespacedName{Name: "new", Namespace: "other-ns"}}
	for _, tc := range []struct {
		name     string
		desired  []Object
		expected map[Object][]types.NamespacedName
	}{
		{"unchanged", []Object{shared, old}, map[Object][]types.NamespacedName{shared: {owner, other}, old: {owner}}},
		{"changed with duplicates", []Object{shared, replacement, replacement}, map[Object][]types.NamespacedName{shared: {owner, other}, replacement: {owner}}},
		{"removed shared dependency", []Object{replacement}, map[Object][]types.NamespacedName{shared: {other}, replacement: {owner}}},
		{"empty", nil, map[Object][]types.NamespacedName{shared: {other}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewResourceWatcher()
			r.watchedResources = map[Object][]types.NamespacedName{shared: {owner, other}, old: {owner}}
			original := &r.watchedResources[shared][0]
			r.SyncDependentWatchedResources(owner, tc.desired)
			require.Equal(t, tc.expected, r.GetWatchedResources())
			if tc.name == "unchanged" {
				assert.Same(t, original, &r.watchedResources[shared][0], "unchanged owners must not be removed and re-added")
			}
			r.SyncDependentWatchedResources(owner, tc.desired)
			assert.Equal(t, tc.expected, r.GetWatchedResources())
		})
	}
}

func TestShouldHandleUpdate(t *testing.T) {
	t.Run("Update shouldn't happen if ConfigMaps data hasn't changed", func(t *testing.T) {
		oldObj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string]string{"testKey": "testValue"},
		}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = "4243"

		assert.False(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
	t.Run("Update should happen if the data has changed for ConfigMap", func(t *testing.T) {
		oldObj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string]string{"testKey": "testValue"},
		}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = "4243"
		newObj.Data["secondKey"] = "secondValue"

		assert.True(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
	t.Run("Update shouldn't happen if Secrets data hasn't changed", func(t *testing.T) {
		oldObj := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string][]byte{"testKey": []byte("testValue")},
		}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = "4243"

		assert.False(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
	t.Run("Update should happen if the data has changed for Secret", func(t *testing.T) {
		oldObj := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string][]byte{"testKey": []byte("testValue")},
		}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = "4243"
		newObj.Data["secondKey"] = []byte("secondValue")

		assert.True(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
}
