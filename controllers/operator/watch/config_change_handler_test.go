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

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
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
	search := types.NamespacedName{Name: "search", Namespace: "ns"}
	labeled := map[string]string{
		khandler.MongoDBSearchOwnerNameLabel:      search.Name,
		khandler.MongoDBSearchOwnerNamespaceLabel: search.Namespace,
	}
	tests := []struct {
		name      string
		obj       client.Object
		resource  Type
		tracked   bool
		mapFunc   func(context.Context, client.Object) []reconcile.Request
		wantQueue int
	}{
		{
			name:      "legacy ConfigMap handler ignores delete",
			obj:       &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state-cm", Namespace: "ns"}},
			resource:  ConfigMap,
			tracked:   true,
			wantQueue: 0,
		},
		{
			name:      "legacy Secret handler ignores delete",
			obj:       &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls-op", Namespace: "ns"}},
			resource:  Secret,
			tracked:   true,
			wantQueue: 0,
		},
		{
			name:      "tracked MongoDB delete routes without map function",
			obj:       &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "mdb", Namespace: "ns"}},
			resource:  MongoDB,
			tracked:   true,
			wantQueue: 1,
		},
		{
			name:      "Search handler routes tracked ConfigMap delete",
			obj:       &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "state-cm", Namespace: "ns"}},
			resource:  ConfigMap,
			tracked:   true,
			mapFunc:   khandler.EnqueueMemberClusterObjectToSearch,
			wantQueue: 1,
		},
		{
			name: "Search handler maps labeled Secret delete",
			obj: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: "tls-op", Namespace: "ns", Labels: labeled,
			}},
			resource:  Secret,
			mapFunc:   khandler.EnqueueMemberClusterObjectToSearch,
			wantQueue: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			watcher := NewResourceWatcher()
			if tc.tracked {
				watcher.AddWatchedResourceIfNotAdded(tc.obj.GetName(), tc.obj.GetNamespace(), tc.resource, search)
			}
			h := &ResourcesHandler{ResourceType: tc.resource, ResourceWatcher: watcher, MapFunc: tc.mapFunc}
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()

			h.Delete(t.Context(), event.TypedDeleteEvent[client.Object]{Object: tc.obj}, q)

			assert.Equal(t, tc.wantQueue, q.Len())
			if tc.wantQueue > 0 {
				req, shutdown := q.Get()
				assert.False(t, shutdown)
				assert.Equal(t, search, req.NamespacedName)
				q.Done(req)
			}
		})
	}
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
