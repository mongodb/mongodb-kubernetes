package watch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestResourcesHandlerEnqueuesConfigMapDependent checks that a data change of a watched CA ConfigMap
// enqueues the resource that depends on it, which for a multi-cluster deployment is the
// MongoDBMultiCluster itself.
func TestResourcesHandlerEnqueuesConfigMapDependent(t *testing.T) {
	dependent := types.NamespacedName{Name: "mdb-mc", Namespace: "ns"}
	watcher := NewResourceWatcher()
	watcher.AddWatchedResourceIfNotAdded("ca-config-map", dependent.Namespace, ConfigMap, dependent)
	handler := ResourcesHandler{ResourceType: ConfigMap, ResourceWatcher: watcher}

	oldObj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "ca-config-map", Namespace: dependent.Namespace},
		Data:       map[string]string{"ca.crt": "old"},
	}

	t.Run("changed data enqueues the dependent", func(t *testing.T) {
		newObj := oldObj.DeepCopy()
		newObj.Data["ca.crt"] = "new"
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer queue.ShutDown()

		handler.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldObj, ObjectNew: newObj}, queue)

		require.Equal(t, 1, queue.Len())
		req, shutdown := queue.Get()
		require.False(t, shutdown)
		assert.Equal(t, dependent, req.NamespacedName)
		queue.Done(req)
	})

	t.Run("unchanged data does not enqueue", func(t *testing.T) {
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = "42"
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer queue.ShutDown()

		handler.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldObj, ObjectNew: newObj}, queue)

		assert.Zero(t, queue.Len())
	})
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
