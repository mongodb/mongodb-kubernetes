package operator

import (
	"context"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	rolev1 "github.com/mongodb/mongodb-kubernetes/api/v1/role"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"testing"
)

func TestReconcileClusterMongoDBRole(t *testing.T) {
	ctx := context.Background()
	role := DefaultClusterMongoDBRoleBuilder().Build()
	reconciler, _ := defaultRoleReconciler(ctx, role)

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})
	if err != nil {
		return
	}

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")
}

func TestEnsureFinalizer(t *testing.T) {
	ctx := context.Background()
	role := DefaultClusterMongoDBRoleBuilder().Build()
	reconciler, fakeClient := defaultRoleReconciler(ctx, role)

	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})
	assert.NoError(t, err)

	err = fakeClient.Get(ctx, types.NamespacedName{Name: role.Name}, role)
	assert.NoError(t, err)

	assert.Contains(t, role.GetFinalizers(), util.RoleFinalizer, "the finalizer should be present")
}

func TestRoleIsRemovedWhenNoReferences(t *testing.T) {
	ctx := context.Background()
	role := DefaultClusterMongoDBRoleBuilder().Build()
	reconciler, fakeClient := defaultRoleReconciler(ctx, role)

	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})
	assert.NoError(t, err)

	err = fakeClient.Delete(ctx, role)
	assert.NoError(t, err)

	newResult, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})

	newExpected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, newExpected, newResult, "there should be a successful reconciliation if the password is a valid reference")

	err = fakeClient.Get(ctx, types.NamespacedName{Name: role.Name}, role)
	assert.True(t, apiErrors.IsNotFound(err), "the role should not exist")
}

func TestRoleIsNotRemovedWhenReferenced(t *testing.T) {
	ctx := context.Background()
	role := DefaultClusterMongoDBRoleBuilder().Build()
	reconciler, fakeClient := defaultRoleReconciler(ctx, role)

	_ = fakeClient.Create(ctx, DefaultReplicaSetBuilder().
		SetRoleRefs([]mdb.MongoDBRoleRef{
			{
				Name: role.Name,
				Kind: util.ClusterMongoDBRoleKind,
			},
		}).
		SetName("my-rs").Build())

	// Add finalizer
	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})
	assert.NoError(t, err)

	// Delete resource, should fail since it is still referenced
	err = fakeClient.Delete(ctx, role)
	assert.NoError(t, err)

	// Should not remove the finalizer
	_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})

	err = fakeClient.Get(ctx, types.NamespacedName{Name: role.Name}, role)
	assert.NoError(t, err, "the role should still exist")
	assert.NotEmpty(t, role.GetFinalizers(), "the finalizer should still be present")
}

func TestRoleIsRemovedAfterRemovingReference(t *testing.T) {
	ctx := context.Background()
	role := DefaultClusterMongoDBRoleBuilder().Build()
	mdbrs := DefaultReplicaSetBuilder().
		SetRoleRefs([]mdb.MongoDBRoleRef{
			{
				Name: role.Name,
				Kind: util.ClusterMongoDBRoleKind,
			},
		}).
		SetName("my-rs").Build()
	reconciler, fakeClient := defaultRoleReconciler(ctx, role)

	_ = fakeClient.Create(ctx, mdbrs)

	// Add finalizer
	_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})

	// Delete resource, should fail since it is still referenced
	_ = fakeClient.Delete(ctx, role)

	// Should not remove the finalizer
	_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})

	err := fakeClient.Get(ctx, types.NamespacedName{Name: role.Name}, role)
	assert.NoError(t, err, "the role should still exist")

	// Remove reference
	mdbrs.Spec.Security.RoleRefs = []mdb.MongoDBRoleRef{}
	err = fakeClient.Update(ctx, mdbrs)

	// Should remove finalizer
	_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: role.Name}})

	// Resource should be removed
	err = fakeClient.Get(ctx, types.NamespacedName{Name: role.Name}, role)
	assert.True(t, apiErrors.IsNotFound(err), "the role should not exist")

}

func defaultRoleReconciler(ctx context.Context, role *rolev1.ClusterMongoDBRole) (*ClusterMongoDBRoleReconciler, client.Client) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(role)
	memberClusterMap := getFakeMultiClusterMap(omConnectionFactory)
	return newClusterMongoDBRoleReconciler(ctx, kubeClient, memberClusterMap), kubeClient
}

type ClusterMongoDBRoleBuilder struct {
	name        string
	finalizers  []string
	annotations map[string]string
	mongoDBRole mdb.MongoDBRole
}

func DefaultClusterMongoDBRoleBuilder() *ClusterMongoDBRoleBuilder {
	return &ClusterMongoDBRoleBuilder{
		name:       "default-role",
		finalizers: []string{},
		mongoDBRole: mdb.MongoDBRole{
			Role:                       "default-role",
			AuthenticationRestrictions: nil,
			Db:                         "admin",
			Privileges:                 nil,
			Roles: []mdb.InheritedRole{
				{
					Role: "readWrite",
					Db:   "admin",
				},
			},
		},
		annotations: map[string]string{},
	}
}

func (b *ClusterMongoDBRoleBuilder) SetName(name string) *ClusterMongoDBRoleBuilder {
	b.name = name
	return b
}

func (b *ClusterMongoDBRoleBuilder) AddFinalizer(finalizer string) *ClusterMongoDBRoleBuilder {
	b.finalizers = append(b.finalizers, finalizer)
	return b
}

func (b *ClusterMongoDBRoleBuilder) SetMongoDBRole(role mdb.MongoDBRole) *ClusterMongoDBRoleBuilder {
	b.mongoDBRole = role
	return b
}

func (b *ClusterMongoDBRoleBuilder) AddAnnotation(key, value string) *ClusterMongoDBRoleBuilder {
	b.annotations[key] = value
	return b
}

func (b *ClusterMongoDBRoleBuilder) Build() *rolev1.ClusterMongoDBRole {
	return &rolev1.ClusterMongoDBRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        b.name,
			Finalizers:  b.finalizers,
			Annotations: b.annotations,
		},
		Spec: rolev1.ClusterMongoDBRoleSpec{
			MongoDBRole: b.mongoDBRole,
		},
	}
}
