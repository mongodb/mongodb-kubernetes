package watch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

func TestResourcesHandlerDelete(t *testing.T) {
	t.Run("configmap delete is ignored", func(t *testing.T) {
		watcher := NewResourceWatcher()
		search := types.NamespacedName{Name: "search", Namespace: "ns"}
		watcher.AddWatchedResourceIfNotAdded("state-cm", "ns", ConfigMap, search)
		handler := &ResourcesHandler{ResourceType: ConfigMap, ResourceWatcher: watcher}
		q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer q.ShutDown()

		handler.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{
			Object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state-cm", Namespace: "ns"}},
		}, q)
		assert.Equal(t, 0, q.Len())
	})

	t.Run("secret delete requeues dependent resource", func(t *testing.T) {
		watcher := NewResourceWatcher()
		search := types.NamespacedName{Name: "search", Namespace: "ns"}
		watcher.AddWatchedResourceIfNotAdded("tls-op", "ns", Secret, search)
		handler := &ResourcesHandler{ResourceType: Secret, ResourceWatcher: watcher}
		q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer q.ShutDown()

		handler.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{
			Object: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls-op", Namespace: "ns"}},
		}, q)
		assert.Equal(t, 1, q.Len())
		req, shutdown := q.Get()
		assert.False(t, shutdown)
		assert.Equal(t, search, req.NamespacedName)
		q.Done(req)
	})
}
