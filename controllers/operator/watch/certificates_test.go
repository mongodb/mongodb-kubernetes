package watch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

func certificateOwnedBy(owner *mdbv1.MongoDB) *certmanagerv1.Certificate {
	return &certmanagerv1.Certificate{ObjectMeta: metav1.ObjectMeta{
		Name:            owner.Name + "-managed-cert",
		Namespace:       owner.Namespace,
		OwnerReferences: kube.BaseOwnerReference(owner),
	}}
}

func TestMongoDBOwnerOfType(t *testing.T) {
	ctx := context.Background()
	rs := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetNamespace(mock.TestNamespace).Build()
	sc := mdbv1.NewDefaultShardedClusterBuilder().SetName("my-sc").SetNamespace(mock.TestNamespace).Build()
	c := mock.NewEmptyFakeClientBuilder().WithObjects(rs, sc).Build()

	toReplicaSet := mongoDBOwnerOfType(c, mdbv1.ReplicaSet)
	toShardedCluster := mongoDBOwnerOfType(c, mdbv1.ShardedCluster)

	t.Run("a replica set's certificate goes only to the replica set controller", func(t *testing.T) {
		certificate := certificateOwnedBy(rs)
		assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: rs.Namespace, Name: rs.Name}}}, toReplicaSet(ctx, certificate))
		assert.Empty(t, toShardedCluster(ctx, certificate))
	})

	t.Run("a sharded cluster's certificate goes only to the sharded cluster controller", func(t *testing.T) {
		certificate := certificateOwnedBy(sc)
		assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: sc.Namespace, Name: sc.Name}}}, toShardedCluster(ctx, certificate))
		assert.Empty(t, toReplicaSet(ctx, certificate))
	})

	t.Run("a certificate whose owner is gone goes nowhere", func(t *testing.T) {
		gone := mdbv1.NewReplicaSetBuilder().SetName("gone").SetNamespace(mock.TestNamespace).Build()
		assert.Empty(t, toReplicaSet(ctx, certificateOwnedBy(gone)))
	})

	t.Run("a certificate without a MongoDB owner goes nowhere", func(t *testing.T) {
		userCertificate := &certmanagerv1.Certificate{ObjectMeta: metav1.ObjectMeta{Name: "user-created", Namespace: mock.TestNamespace}}
		assert.Empty(t, toReplicaSet(ctx, userCertificate))

		// An owner of another kind, with the same name as a replica set, is not that replica set.
		otherKind := certificateOwnedBy(rs)
		otherKind.OwnerReferences[0].Kind = "MongoDBMultiCluster"
		require.Equal(t, rs.Name, otherKind.OwnerReferences[0].Name)
		assert.Empty(t, toReplicaSet(ctx, otherKind))
	})
}
