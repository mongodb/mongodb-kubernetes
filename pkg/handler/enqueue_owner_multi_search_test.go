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

func labeledConfigMap(ns, name string, labels map[string]string) client.Object {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
	}
}

func ownerLabels(name, ns string) map[string]string {
	return map[string]string{
		MongoDBSearchOwnerNameLabel:      name,
		MongoDBSearchOwnerNamespaceLabel: ns,
	}
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Create_LabelsPresent_Enqueues(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := labeledConfigMap("ns1", "mongot-cm", ownerLabels("my-search", "ns1"))
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 1, q.Len())
	got, _ := q.Get()
	assert.Equal(t, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns1", Name: "my-search"}}, got)
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Create_LabelsMissing_NoEnqueue(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	// Object with no labels at all.
	obj := labeledConfigMap("ns1", "unrelated", nil)
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 0, q.Len())
}

// TestEnqueueRequestForSearchOwnerMultiCluster_Create_PartialLabel_NoEnqueue
// asserts that only the name label (without the namespace label) is not enough
// — both must be present, otherwise the mapper cannot uniquely route the event.
func TestEnqueueRequestForSearchOwnerMultiCluster_Create_PartialLabel_NoEnqueue(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := labeledConfigMap("ns1", "cm", map[string]string{
		MongoDBSearchOwnerNameLabel: "my-search",
		// MongoDBSearchOwnerNamespaceLabel intentionally missing
	})
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 0, q.Len(), "partial label must not enqueue — both name and namespace required")
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Update_BothLabeled_EnqueuesBoth(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	oldObj := labeledConfigMap("ns1", "cm", ownerLabels("old-owner", "ns1"))
	newObj := labeledConfigMap("ns1", "cm", ownerLabels("new-owner", "ns1"))

	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: oldObj, ObjectNew: newObj}, q)

	assert.Equal(t, 2, q.Len(), "both old and new owners must be enqueued so the leaving CR can clean up")
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Delete_LabelsPresent_Enqueues(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := labeledConfigMap("ns1", "cm", ownerLabels("my-search", "ns1"))
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 1, q.Len())
}

func TestEnqueueRequestForSearchOwnerMultiCluster_Generic_NoEnqueue(t *testing.T) {
	q := newQueue()
	defer q.ShutDown()

	obj := labeledConfigMap("ns1", "cm", ownerLabels("my-search", "ns1"))
	h := &EnqueueRequestForSearchOwnerMultiCluster{}
	h.Generic(context.Background(), event.TypedGenericEvent[client.Object]{Object: obj}, q)

	assert.Equal(t, 0, q.Len(), "generic events do not trigger reconciles for owner-tracking handlers")
}
