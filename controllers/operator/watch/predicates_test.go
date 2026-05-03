package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/pkg/handler"
)

func statefulSet(annotations map[string]string, replicas, readyReplicas int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Annotations: annotations, Generation: 1},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To(replicas)},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 1,
			Replicas:           replicas,
			UpdatedReplicas:    readyReplicas,
			ReadyReplicas:      readyReplicas,
		},
	}
}

func TestPredicatesForUser(t *testing.T) {
	t.Run("No reconciliation for MongoDBUser if statuses are not equal", func(t *testing.T) {
		oldUser := &user.MongoDBUser{
			Status: user.MongoDBUserStatus{},
		}
		newUser := oldUser.DeepCopy()
		newUser.Status.Phase = status.PhasePending
		assert.False(t, PredicatesForUser().Update(event.UpdateEvent{ObjectOld: oldUser, ObjectNew: newUser}))
	})
	t.Run("Reconciliation happens for MongoDBUser if statuses are equal", func(t *testing.T) {
		oldUser := &user.MongoDBUser{
			Status: user.MongoDBUserStatus{},
		}
		newUser := oldUser.DeepCopy()
		newUser.Spec.Username = "test"
		assert.True(t, PredicatesForUser().Update(event.UpdateEvent{ObjectOld: oldUser, ObjectNew: newUser}))
	})
}

func TestPredicatesForOpsManager(t *testing.T) {
	t.Run("No reconciliation for MongoDBOpsManager if statuses are not equal", func(t *testing.T) {
		oldOm := omv1.NewOpsManagerBuilder().Build()
		newOm := oldOm.DeepCopy()
		newOm.Spec.Replicas = 2
		newOm.Status.OpsManagerStatus = omv1.OpsManagerStatus{Warnings: []status.Warning{"warning"}}
		assert.False(t, PredicatesForOpsManager().Update(event.UpdateEvent{ObjectOld: oldOm, ObjectNew: newOm}))
	})
	t.Run("Reconciliation happens for MongoDBOpsManager if statuses are equal", func(t *testing.T) {
		oldOm := omv1.NewOpsManagerBuilder().Build()
		newOm := oldOm.DeepCopy()
		newOm.Spec.Replicas = 2
		assert.True(t, PredicatesForOpsManager().Update(event.UpdateEvent{ObjectOld: oldOm, ObjectNew: newOm}))
	})
}

func TestPredicatesForMongoDB(t *testing.T) {
	t.Run("Creation event is handled", func(t *testing.T) {
		standalone := mdbv1.NewStandaloneBuilder().Build()
		assert.True(t, PredicatesForMongoDB(mdbv1.Standalone).Create(event.CreateEvent{Object: standalone}))
	})
	t.Run("Creation event is not handled", func(t *testing.T) {
		rs := mdbv1.NewReplicaSetBuilder().Build()
		assert.False(t, PredicatesForMongoDB(mdbv1.Standalone).Create(event.CreateEvent{Object: rs}))
	})
	t.Run("Delete event is handled", func(t *testing.T) {
		sc := mdbv1.NewClusterBuilder().Build()
		assert.True(t, PredicatesForMongoDB(mdbv1.ShardedCluster).Delete(event.DeleteEvent{Object: sc}))
	})
	t.Run("Delete event is not handled", func(t *testing.T) {
		rs := mdbv1.NewReplicaSetBuilder().Build()
		assert.False(t, PredicatesForMongoDB(mdbv1.ShardedCluster).Delete(event.DeleteEvent{Object: rs}))
	})
	t.Run("Update event is handled, statuses not changed", func(t *testing.T) {
		oldMdb := mdbv1.NewStandaloneBuilder().Build()
		newMdb := oldMdb.DeepCopy()
		newMdb.Spec.Version = "4.2.0"
		assert.True(t, PredicatesForMongoDB(mdbv1.Standalone).Update(
			event.UpdateEvent{ObjectOld: oldMdb, ObjectNew: newMdb}),
		)
	})
	t.Run("Update event is not handled, statuses changed", func(t *testing.T) {
		oldMdb := mdbv1.NewStandaloneBuilder().Build()
		newMdb := oldMdb.DeepCopy()
		newMdb.Status.Version = "4.2.0"
		assert.False(t, PredicatesForMongoDB(mdbv1.Standalone).Update(
			event.UpdateEvent{ObjectOld: oldMdb, ObjectNew: newMdb}),
		)
	})
	t.Run("Update event is not handled, different types", func(t *testing.T) {
		oldMdb := mdbv1.NewStandaloneBuilder().Build()
		newMdb := oldMdb.DeepCopy()
		newMdb.Spec.Version = "4.2.0"
		assert.False(t, PredicatesForMongoDB(mdbv1.ShardedCluster).Update(
			event.UpdateEvent{ObjectOld: oldMdb, ObjectNew: newMdb}),
		)
	})
}

// TestPredicatesForStatefulSet covers the single-cluster replica set StatefulSet watch. It only
// fires when the StatefulSet is annotated as a Replicaset and the status changed.
func TestPredicatesForStatefulSet(t *testing.T) {
	rsAnnotations := map[string]string{"type": "Replicaset"}
	t.Run("Fires when status changed and not all replicas ready", func(t *testing.T) {
		oldSts := statefulSet(rsAnnotations, 3, 3)
		newSts := statefulSet(rsAnnotations, 3, 2)
		assert.True(t, PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire when status unchanged", func(t *testing.T) {
		oldSts := statefulSet(rsAnnotations, 3, 2)
		newSts := statefulSet(rsAnnotations, 3, 2)
		assert.False(t, PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Fires when StatefulSet transitions back to ready", func(t *testing.T) {
		oldSts := statefulSet(rsAnnotations, 3, 2)
		newSts := statefulSet(rsAnnotations, 3, 3)
		assert.True(t, PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire for ready-to-ready status noise", func(t *testing.T) {
		oldSts := statefulSet(rsAnnotations, 3, 3)
		newSts := statefulSet(rsAnnotations, 3, 3)
		newSts.Status.AvailableReplicas = 3
		assert.False(t, PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Fires when StatefulSet current replicas settle", func(t *testing.T) {
		oldSts := statefulSet(rsAnnotations, 3, 3)
		oldSts.Status.Replicas = 4
		newSts := statefulSet(rsAnnotations, 3, 3)
		assert.True(t, PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire for a different type annotation", func(t *testing.T) {
		oldSts := statefulSet(map[string]string{"type": "ShardedCluster"}, 3, 3)
		newSts := statefulSet(map[string]string{"type": "ShardedCluster"}, 3, 2)
		assert.False(t, PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Non-update events are not handled", func(t *testing.T) {
		sts := statefulSet(rsAnnotations, 3, 2)
		assert.False(t, PredicatesForStatefulSet().Create(event.CreateEvent{Object: sts}))
		assert.False(t, PredicatesForStatefulSet().Delete(event.DeleteEvent{Object: sts}))
		assert.False(t, PredicatesForStatefulSet().Generic(event.GenericEvent{Object: sts}))
	})
}

// TestPredicatesForShardedStatefulSet covers the single-cluster sharded StatefulSet watch. It only
// fires when the StatefulSet is annotated as a ShardedCluster and the status changed.
func TestPredicatesForShardedStatefulSet(t *testing.T) {
	shardedAnnotations := map[string]string{"type": "ShardedCluster"}
	t.Run("Fires when status changed and not all replicas ready", func(t *testing.T) {
		oldSts := statefulSet(shardedAnnotations, 3, 3)
		newSts := statefulSet(shardedAnnotations, 3, 2)
		assert.True(t, PredicatesForShardedStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire when status unchanged", func(t *testing.T) {
		oldSts := statefulSet(shardedAnnotations, 3, 2)
		newSts := statefulSet(shardedAnnotations, 3, 2)
		assert.False(t, PredicatesForShardedStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Fires when StatefulSet transitions back to ready", func(t *testing.T) {
		oldSts := statefulSet(shardedAnnotations, 3, 2)
		newSts := statefulSet(shardedAnnotations, 3, 3)
		assert.True(t, PredicatesForShardedStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire for ready-to-ready status noise", func(t *testing.T) {
		oldSts := statefulSet(shardedAnnotations, 3, 3)
		newSts := statefulSet(shardedAnnotations, 3, 3)
		newSts.Status.AvailableReplicas = 3
		assert.False(t, PredicatesForShardedStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Fires when StatefulSet current replicas settle", func(t *testing.T) {
		oldSts := statefulSet(shardedAnnotations, 3, 3)
		oldSts.Status.Replicas = 4
		newSts := statefulSet(shardedAnnotations, 3, 3)
		assert.True(t, PredicatesForShardedStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire for a different type annotation", func(t *testing.T) {
		oldSts := statefulSet(map[string]string{"type": "Replicaset"}, 3, 3)
		newSts := statefulSet(map[string]string{"type": "Replicaset"}, 3, 2)
		assert.False(t, PredicatesForShardedStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Non-update events are not handled", func(t *testing.T) {
		sts := statefulSet(shardedAnnotations, 3, 2)
		assert.False(t, PredicatesForShardedStatefulSet().Create(event.CreateEvent{Object: sts}))
		assert.False(t, PredicatesForShardedStatefulSet().Delete(event.DeleteEvent{Object: sts}))
		assert.False(t, PredicatesForShardedStatefulSet().Generic(event.GenericEvent{Object: sts}))
	})
}

// TestPredicatesForOpsManagerStatefulSet covers the single-cluster Ops Manager / AppDB StatefulSet
// watch. Ownership is resolved by the owner handler, so the predicate does not gate on a "type"
// annotation. It fires when the status changed.
func TestPredicatesForOpsManagerStatefulSet(t *testing.T) {
	t.Run("Fires when status changed and not all replicas ready", func(t *testing.T) {
		oldSts := statefulSet(nil, 3, 3)
		newSts := statefulSet(nil, 3, 2)
		assert.True(t, PredicatesForOpsManagerStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire when status unchanged", func(t *testing.T) {
		oldSts := statefulSet(nil, 3, 2)
		newSts := statefulSet(nil, 3, 2)
		assert.False(t, PredicatesForOpsManagerStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Fires when StatefulSet transitions back to ready", func(t *testing.T) {
		oldSts := statefulSet(nil, 3, 2)
		newSts := statefulSet(nil, 3, 3)
		assert.True(t, PredicatesForOpsManagerStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Does not fire for ready-to-ready status noise", func(t *testing.T) {
		oldSts := statefulSet(nil, 3, 3)
		newSts := statefulSet(nil, 3, 3)
		newSts.Status.AvailableReplicas = 3
		assert.False(t, PredicatesForOpsManagerStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Fires when StatefulSet current replicas settle", func(t *testing.T) {
		oldSts := statefulSet(nil, 3, 3)
		oldSts.Status.Replicas = 4
		newSts := statefulSet(nil, 3, 3)
		assert.True(t, PredicatesForOpsManagerStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Non-update events are not handled", func(t *testing.T) {
		sts := statefulSet(nil, 3, 2)
		assert.False(t, PredicatesForOpsManagerStatefulSet().Create(event.CreateEvent{Object: sts}))
		assert.False(t, PredicatesForOpsManagerStatefulSet().Delete(event.DeleteEvent{Object: sts}))
		assert.False(t, PredicatesForOpsManagerStatefulSet().Generic(event.GenericEvent{Object: sts}))
	})
}

// TestPredicatesForMultiStatefulSet covers the multi-cluster member StatefulSet watch used by the
// Ops Manager / AppDB and sharded reconcilers. It fires on any status change of a StatefulSet that
// carries the MongoDBMultiResource annotation, and handles delete events for those StatefulSets.
func TestPredicatesForMultiStatefulSet(t *testing.T) {
	multiAnnotations := map[string]string{handler.MongoDBMultiResourceAnnotation: "my-resource"}
	t.Run("Update fires when annotated and status changed", func(t *testing.T) {
		oldSts := statefulSet(multiAnnotations, 3, 3)
		newSts := statefulSet(multiAnnotations, 3, 2)
		assert.True(t, PredicatesForMultiStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Update does not fire when status unchanged", func(t *testing.T) {
		oldSts := statefulSet(multiAnnotations, 3, 2)
		newSts := statefulSet(multiAnnotations, 3, 2)
		assert.False(t, PredicatesForMultiStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Update does not fire without the multi annotation", func(t *testing.T) {
		oldSts := statefulSet(nil, 3, 3)
		newSts := statefulSet(nil, 3, 2)
		assert.False(t, PredicatesForMultiStatefulSet().Update(event.UpdateEvent{ObjectOld: oldSts, ObjectNew: newSts}))
	})
	t.Run("Delete fires for an annotated StatefulSet", func(t *testing.T) {
		sts := statefulSet(multiAnnotations, 3, 3)
		assert.True(t, PredicatesForMultiStatefulSet().Delete(event.DeleteEvent{Object: sts}))
	})
	t.Run("Delete does not fire without the multi annotation", func(t *testing.T) {
		sts := statefulSet(nil, 3, 3)
		assert.False(t, PredicatesForMultiStatefulSet().Delete(event.DeleteEvent{Object: sts}))
	})
	t.Run("Create and generic events are not handled", func(t *testing.T) {
		sts := statefulSet(multiAnnotations, 3, 3)
		assert.False(t, PredicatesForMultiStatefulSet().Create(event.CreateEvent{Object: sts}))
		assert.False(t, PredicatesForMultiStatefulSet().Generic(event.GenericEvent{Object: sts}))
	})
}

func labeledSearchObj(labels map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns", Labels: labels}}
}

func searchOwnerLabels(name, ns string) map[string]string {
	return map[string]string{
		handler.MongoDBSearchOwnerNameLabel:      name,
		handler.MongoDBSearchOwnerNamespaceLabel: ns,
	}
}

func TestPredicatesForMultiClusterSearchResource(t *testing.T) {
	p := PredicatesForMultiClusterSearchResource()
	labeled := labeledSearchObj(searchOwnerLabels("s", "ns"))
	plain := labeledSearchObj(nil)
	unrelated := labeledSearchObj(map[string]string{"unrelated": "x"})
	partialName := labeledSearchObj(map[string]string{handler.MongoDBSearchOwnerNameLabel: "s"})
	partialNs := labeledSearchObj(map[string]string{handler.MongoDBSearchOwnerNamespaceLabel: "ns"})

	tests := []struct {
		name string
		want bool
		eval func() bool
	}{
		// Create
		{"Create/labeled passes", true, func() bool { return p.CreateFunc(event.CreateEvent{Object: labeled}) }},
		{"Create/plain fails", false, func() bool { return p.CreateFunc(event.CreateEvent{Object: plain}) }},
		{"Create/unrelated fails", false, func() bool { return p.CreateFunc(event.CreateEvent{Object: unrelated}) }},
		{"Create/partial name-only fails", false, func() bool { return p.CreateFunc(event.CreateEvent{Object: partialName}) }},
		{"Create/partial ns-only fails", false, func() bool { return p.CreateFunc(event.CreateEvent{Object: partialNs}) }},
		// Update
		{"Update/both labeled passes", true, func() bool { return p.UpdateFunc(event.UpdateEvent{ObjectOld: labeled, ObjectNew: labeled}) }},
		{"Update/newly labeled passes", true, func() bool { return p.UpdateFunc(event.UpdateEvent{ObjectOld: plain, ObjectNew: labeled}) }},
		{"Update/label removed passes", true, func() bool { return p.UpdateFunc(event.UpdateEvent{ObjectOld: labeled, ObjectNew: plain}) }},
		{"Update/both plain fails", false, func() bool { return p.UpdateFunc(event.UpdateEvent{ObjectOld: plain, ObjectNew: plain}) }},
		// Delete
		{"Delete/labeled passes", true, func() bool { return p.DeleteFunc(event.DeleteEvent{Object: labeled}) }},
		{"Delete/plain fails", false, func() bool { return p.DeleteFunc(event.DeleteEvent{Object: plain}) }},
		// Generic
		{"Generic/labeled always false", false, func() bool { return p.GenericFunc(event.GenericEvent{Object: labeled}) }},
		{"Generic/plain always false", false, func() bool { return p.GenericFunc(event.GenericEvent{Object: plain}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.eval())
		})
	}
}
