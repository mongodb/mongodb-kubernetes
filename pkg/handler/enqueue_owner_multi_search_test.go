package handler

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

func newQueue() workqueue.TypedRateLimitingInterface[reconcile.Request] {
	return workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
}

func newAnnotatedConfigMap(ns, name string, ann map[string]string) client.Object {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: ann},
	}
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Create_AnnotationPresent_Enqueues(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := newAnnotatedConfigMap("ns1", "mongot-cm", map[string]string{
		MongoDBSearchResourceAnnotation: "my-search",
	})
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 1, q.Len())
	got, _ := q.Get()
	assert.Equal(t, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "my-search"}}, got)
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Create_AnnotationMissing_NoEnqueue(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := newAnnotatedConfigMap("ns1", "unrelated", nil)
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 0, q.Len())
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Update_BothAnnotated_EnqueuesBoth(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	oldObj := newAnnotatedConfigMap("ns1", "cm", map[string]string{MongoDBSearchResourceAnnotation: "old-owner"})
	newObj := newAnnotatedConfigMap("ns1", "cm", map[string]string{MongoDBSearchResourceAnnotation: "new-owner"})

	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldObj, ObjectNew: newObj}, q)

	assert.Equal(t, 2, q.Len(), "both old and new owners must be enqueued so the leaving CR can clean up")
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Delete_AnnotationPresent_Enqueues(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := newAnnotatedConfigMap("ns1", "cm", map[string]string{MongoDBSearchResourceAnnotation: "my-search"})
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 1, q.Len())
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Generic_NoEnqueue(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := newAnnotatedConfigMap("ns1", "cm", map[string]string{MongoDBSearchResourceAnnotation: "my-search"})
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Generic(context.Background(), event.TypedGenericEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 0, q.Len(), "generic events do not trigger reconciles for owner-tracking handlers")
}
