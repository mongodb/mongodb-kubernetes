package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// TestDeleteManagedCertificateObjectsInCluster covers the cleanup that removes what operator-managed
// certificates created for a deleted resource. It can still be running when a resource with the same
// name is created again, so it must remove only what was created for the deleted resource, and only
// the secrets and ConfigMaps the feature creates.
func TestDeleteManagedCertificateObjectsInCluster(t *testing.T) {
	ctx := context.Background()

	managedReplicaSet := func(uid types.UID) *mdbv1.MongoDB {
		rs := mdbv1.NewReplicaSetBuilder().
			SetName("my-rs").
			SetNamespace(mock.TestNamespace).
			SetSecurity(&mdbv1.Security{
				TLSConfig:          &mdbv1.TLSConfig{Enabled: true},
				ManagedCertificate: &mdbv1.ManagedCertificate{},
			}).
			Build()
		rs.SetUID(uid)
		return rs
	}
	// The deleted resource and the one created again with the same name.
	deleted := managedReplicaSet("deleted-uid")
	recreated := managedReplicaSet("recreated-uid")

	meta := func(name, namespace string, labels map[string]string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}
	}
	deletedLabels := certs.ManagedCertificateOwnerLabels(deleted)
	recreatedLabels := certs.ManagedCertificateOwnerLabels(recreated)
	objects := func() []client.Object {
		return []client.Object{
			&corev1.Secret{ObjectMeta: meta("deleted-cert", mock.TestNamespace, deletedLabels)},
			&corev1.ConfigMap{ObjectMeta: meta("deleted-ca-bundle", mock.TestNamespace, deletedLabels)},
			&corev1.Secret{ObjectMeta: meta("recreated-cert", mock.TestNamespace, recreatedLabels)},
			&corev1.ConfigMap{ObjectMeta: meta("recreated-ca-bundle", mock.TestNamespace, recreatedLabels)},
			// The other objects of the resource carry the labels that name it, which the two resources share.
			&appsv1.StatefulSet{ObjectMeta: meta("my-rs", mock.TestNamespace, recreated.GetOwnerLabels()), Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3)}},
			&corev1.Service{ObjectMeta: meta("my-rs-svc", mock.TestNamespace, recreated.GetOwnerLabels())},
			&corev1.Secret{ObjectMeta: meta("my-rs-named-secret", mock.TestNamespace, recreated.GetOwnerLabels())},
			// The same labels in another namespace belong to another resource.
			&corev1.Secret{ObjectMeta: meta("deleted-cert", "other-namespace", deletedLabels)},
		}
	}
	exists := func(t *testing.T, c client.Client, obj client.Object) bool {
		t.Helper()
		err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object))
		if apiErrors.IsNotFound(err) {
			return false
		}
		require.NoError(t, err)
		return true
	}

	t.Run("only the deleted resource's secrets and ConfigMaps are removed", func(t *testing.T) {
		all := objects()
		c, _ := mock.NewDefaultFakeClient(all...)

		require.NoError(t, deleteManagedCertificateObjectsInCluster(ctx, c, "my-cluster", deleted, zap.S()))

		removed := all[:2]
		kept := all[2:]
		for _, obj := range removed {
			assert.False(t, exists(t, c, obj), "%T %s/%s has to be removed", obj, obj.GetNamespace(), obj.GetName())
		}
		for _, obj := range kept {
			assert.True(t, exists(t, c, obj), "%T %s/%s has to stay", obj, obj.GetNamespace(), obj.GetName())
		}
	})

	t.Run("nothing is removed for a resource without operator-managed certificates", func(t *testing.T) {
		all := objects()
		c, _ := mock.NewDefaultFakeClient(all...)
		unmanaged := deleted.DeepCopy()
		unmanaged.Spec.Security.ManagedCertificate = nil

		require.NoError(t, deleteManagedCertificateObjectsInCluster(ctx, c, "my-cluster", unmanaged, zap.S()))

		for _, obj := range all {
			assert.True(t, exists(t, c, obj), "%T %s/%s", obj, obj.GetNamespace(), obj.GetName())
		}
	})

	t.Run("nothing is removed for a resource without a UID", func(t *testing.T) {
		all := append(objects(), &corev1.Secret{ObjectMeta: meta("no-uid-cert", mock.TestNamespace, certs.ManagedCertificateOwnerLabels(managedReplicaSet("")))})
		c, _ := mock.NewDefaultFakeClient(all...)

		require.NoError(t, deleteManagedCertificateObjectsInCluster(ctx, c, "my-cluster", managedReplicaSet(""), zap.S()))

		for _, obj := range all {
			assert.True(t, exists(t, c, obj), "%T %s/%s", obj, obj.GetNamespace(), obj.GetName())
		}
	})
}

// TestMultiClusterReplicaSetDeleteRemovesManagedCertificateObjectsInMemberClusters covers the member
// clusters of a multi-cluster replica set. The -pem secrets the pods mount and the copies of the CA
// bundles there cannot point at the resource, which lives in another cluster, so the delete handler
// removes them by label. What a resource created again with the same name has there stays.
func TestMultiClusterReplicaSetDeleteRemovesManagedCertificateObjectsInMemberClusters(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetSecurity(&mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}, ManagedCertificate: &mdbv1.ManagedCertificate{}}).
		Build()
	mrs.SetUID("deleted-uid")

	reconciler, kubeClient, memberClients, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs, architectures.NonStatic)
	_, err := reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, mrs.Namespace)
	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), mrs))
	require.Equal(t, status.PhaseRunning, mrs.Status.Phase, mrs.Status.Message)

	pemSecret := mrs.Spec.GetSecurity().MemberCertificateSecretName(mrs.Name) + certs.OperatorGeneratedCertSuffix
	caBundle := certs.ManagedServerCABundleConfigMapName(mrs.Name)
	recreatedLabels := certs.ManagedCertificateOwnerLabels(&metav1.ObjectMeta{UID: "recreated-uid"})
	for clusterName, c := range memberClients {
		require.NoError(t, c.Get(ctx, kube.ObjectKey(mrs.Namespace, pemSecret), &corev1.Secret{}), "the -pem secret has to be in %s", clusterName)
		require.NoError(t, c.Get(ctx, kube.ObjectKey(mrs.Namespace, caBundle), &corev1.ConfigMap{}), "the CA bundle copy has to be in %s", clusterName)
		require.NoError(t, c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "recreated-cert-pem", Namespace: mrs.Namespace, Labels: recreatedLabels}}))
	}

	require.NoError(t, reconciler.deleteManagedResources(ctx, *mrs, zap.S()))

	for clusterName, c := range memberClients {
		err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, pemSecret), &corev1.Secret{})
		assert.True(t, apiErrors.IsNotFound(err), "the -pem secret has to be removed from %s: %v", clusterName, err)
		err = c.Get(ctx, kube.ObjectKey(mrs.Namespace, caBundle), &corev1.ConfigMap{})
		assert.True(t, apiErrors.IsNotFound(err), "the CA bundle copy has to be removed from %s: %v", clusterName, err)
		assert.NoError(t, c.Get(ctx, kube.ObjectKey(mrs.Namespace, "recreated-cert-pem"), &corev1.Secret{}),
			"what the resource created again with the same name has in %s has to stay", clusterName)
	}
}
