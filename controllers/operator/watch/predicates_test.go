package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/api/v1/user"
)

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
