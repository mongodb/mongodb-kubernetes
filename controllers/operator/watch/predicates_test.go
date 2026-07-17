package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const vaultSecretBackendEnvVar = "SECRET_BACKEND"

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
	t.Run("No reconciliation for MongoDBOpsManager with ExternalApplicationDatabaseRef when vault backend and nothing meaningful changed", func(t *testing.T) {
		t.Setenv(vaultSecretBackendEnvVar, "VAULT_BACKEND")
		oldOm := omv1.NewOpsManagerBuilder().Build()
		oldOm.Spec.ExternalApplicationDatabaseRef = &omv1.ExternalApplicationDatabaseRef{Name: "external-appdb"}
		newOm := oldOm.DeepCopy()
		assert.False(t, PredicatesForOpsManager().Update(event.UpdateEvent{ObjectOld: oldOm, ObjectNew: newOm}))
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

func TestPredicatesForStatefulSet(t *testing.T) {
	buildSts := func(annotations map[string]string, readyReplicas int32) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "my-om-db", Annotations: annotations},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: readyReplicas},
		}
	}

	tests := []struct {
		name            string
		oldSts          *appsv1.StatefulSet
		newSts          *appsv1.StatefulSet
		expectedForward bool
	}{
		{
			name:            "reverse-migration release request added: forwarded",
			oldSts:          buildSts(nil, 3),
			newSts:          buildSts(map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}, 3),
			expectedForward: true,
		},
		{
			name:            "reverse-migration release request removed: forwarded",
			oldSts:          buildSts(map[string]string{util.AppDBReverseMigrationReadyAnnotation: "true"}, 3),
			newSts:          buildSts(nil, 3),
			expectedForward: true,
		},
		{
			name:            "unrelated annotation change: filtered",
			oldSts:          buildSts(map[string]string{"type": "Replicaset"}, 3),
			newSts:          buildSts(map[string]string{"type": "Replicaset", "unrelated": "x"}, 3),
			expectedForward: false,
		},
		{
			name:            "readiness change with type Replicaset: forwarded",
			oldSts:          buildSts(map[string]string{"type": "Replicaset"}, 2),
			newSts:          buildSts(map[string]string{"type": "Replicaset"}, 3),
			expectedForward: true,
		},
		{
			name:            "readiness change without type annotation: filtered",
			oldSts:          buildSts(nil, 2),
			newSts:          buildSts(nil, 3),
			expectedForward: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedForward,
				PredicatesForStatefulSet().Update(event.UpdateEvent{ObjectOld: tt.oldSts, ObjectNew: tt.newSts}))
		})
	}
}
