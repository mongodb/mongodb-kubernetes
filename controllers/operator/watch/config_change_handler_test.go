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

	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
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
	t.Run("configmap delete requeues dependent resource", func(t *testing.T) {
		watcher := NewResourceWatcher()
		search := types.NamespacedName{Name: "search", Namespace: "ns"}
		watcher.AddWatchedResourceIfNotAdded("state-cm", "ns", ConfigMap, search)
		handler := &ResourcesHandler{ResourceType: ConfigMap, ResourceWatcher: watcher}
		q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
		defer q.ShutDown()

		handler.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{
			Object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state-cm", Namespace: "ns"}},
		}, q)
		assert.Equal(t, 1, q.Len())
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

func TestResourcesHandlerMapFuncEvents(t *testing.T) {
	search := types.NamespacedName{Name: "search", Namespace: "ns"}
	labeled := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "owned",
		Namespace: "ns",
		Labels: map[string]string{
			khandler.MongoDBSearchOwnerNameLabel:      search.Name,
			khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
		},
	}}
	plain := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "ns"}}

	tests := []struct {
		name string
		send func(*ResourcesHandler, workqueue.TypedRateLimitingInterface[reconcile.Request])
	}{
		{
			name: "create",
			send: func(h *ResourcesHandler, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Create(t.Context(), event.TypedCreateEvent[client.Object]{Object: labeled}, q)
			},
		},
		{
			name: "update adds labels",
			send: func(h *ResourcesHandler, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Update(t.Context(), event.TypedUpdateEvent[client.Object]{ObjectOld: plain, ObjectNew: labeled}, q)
			},
		},
		{
			name: "update removes labels",
			send: func(h *ResourcesHandler, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Update(t.Context(), event.TypedUpdateEvent[client.Object]{ObjectOld: labeled, ObjectNew: plain}, q)
			},
		},
		{
			name: "delete",
			send: func(h *ResourcesHandler, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Delete(t.Context(), event.TypedDeleteEvent[client.Object]{Object: labeled}, q)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			h := &ResourcesHandler{
				ResourceType:    ConfigMap,
				ResourceWatcher: NewResourceWatcher(),
				MapFunc:         khandler.EnqueueMemberClusterObjectToSearch,
			}

			tc.send(h, q)

			assert.Equal(t, 1, q.Len())
			req, shutdown := q.Get()
			assert.False(t, shutdown)
			assert.Equal(t, search, req.NamespacedName)
			q.Done(req)
		})
	}
}
