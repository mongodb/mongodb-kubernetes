package operator

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// uxSetCertificateLeaf plays cert-manager writing the leaf of a Certificate, signed by the given CA.
func uxSetCertificateLeaf(t *testing.T, ctx context.Context, c kubernetesClient.Client, namespace, secretName string, ca, caKey []byte) {
	t.Helper()
	leaf := issueLeafSignedByCA(t, ca, caKey, secretName)
	secret, err := c.GetSecret(ctx, kube.ObjectKey(namespace, secretName))
	if err != nil {
		require.True(t, apiErrors.IsNotFound(err), "unexpected error: %v", err)
		require.NoError(t, c.CreateSecret(ctx, corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: leaf},
		}))
		return
	}
	secret.Data[corev1.TLSCertKey] = leaf
	require.NoError(t, c.UpdateSecret(ctx, secret))
}

func TestReplicaSetManagedCertificate_UserClientCertificateFollowsCARotation(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}, InternalCluster: util.X509},
	}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, omConnectionFactory.GetConnectionFunc, certs.CertManagerReadiness{Ready: true})
	addKubernetesTlsResources(ctx, kubeClient, rs)

	// The first reconcile creates the Certificates, then cert-manager issues them.
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, rs.Namespace)

	// The user controller creates the client certificate of an application; the client CA signs it
	// because internal cluster authentication uses x509.
	user := &userv1.MongoDBUser{Spec: userv1.MongoDBUserSpec{
		Database:           "$external",
		MongoDBResourceRef: userv1.MongoDBResourceRef{Name: rs.Name},
		ClientCertificate:  &userv1.ClientCertificate{},
	}}
	user.Name = "myapp"
	user.Namespace = rs.Namespace
	require.NoError(t, kubeClient.Create(ctx, user))
	userCert, err := certs.ClientCertificate(rs, user)
	require.NoError(t, err)
	require.NoError(t, kubeClient.Create(ctx, userCert))
	userCertKey := kube.ObjectKey(rs.Namespace, user.ClientCertificateName())

	clientIssuer := rs.Name + "-client-ca-issuer"
	require.Equal(t, clientIssuer, userCert.Spec.IssuerRef.Name)

	// clientLeafSecrets are the secrets of the Certificates the client CA signs, the user's included.
	certList := &certmanagerv1.CertificateList{}
	require.NoError(t, kubeClient.List(ctx, certList))
	var clientLeafSecrets []string
	for _, cert := range certList.Items {
		if cert.Spec.IssuerRef.Name == clientIssuer {
			clientLeafSecrets = append(clientLeafSecrets, cert.Spec.SecretName)
		}
	}
	require.Contains(t, clientLeafSecrets, user.ClientCertificateSecretName())
	require.Greater(t, len(clientLeafSecrets), 1)

	setClientRoot := func(ca []byte) {
		caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-client-ca"))
		require.NoError(t, err)
		caSecret.Data[corev1.ServiceAccountRootCAKey] = ca
		caSecret.Data[corev1.TLSCertKey] = ca
		require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))
	}
	reconcile := func() {
		_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
	}
	clientBundle := func() string {
		cm, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(rs.Namespace, certs.ManagedClientCABundleConfigMapName(rs.Name)))
		require.NoError(t, err)
		return cm.Data[cm.Data[util.LatestHashSecretKey]]
	}
	userCertificateIssuing := func() *certmanagerv1.CertificateCondition {
		cert := &certmanagerv1.Certificate{}
		require.NoError(t, kubeClient.Get(ctx, userCertKey, cert))
		for i := range cert.Status.Conditions {
			if cert.Status.Conditions[i].Type == certmanagerv1.CertificateConditionIssuing {
				return &cert.Status.Conditions[i]
			}
		}
		return nil
	}

	// Start from a controlled client root, every client leaf signed by it.
	oldCA, oldCAKey := createMockCertAndKeyBytes()
	newCA, newCAKey := createMockCertAndKeyBytes()
	setClientRoot(oldCA)
	for _, name := range clientLeafSecrets {
		uxSetCertificateLeaf(t, ctx, kubeClient, rs.Namespace, name, oldCA, oldCAKey)
	}
	reconcile()
	assert.Nil(t, userCertificateIssuing(), "no rotation yet, no reissue")

	// cert-manager renews the client root: the operator asks for the user certificate to be reissued.
	setClientRoot(newCA)
	reconcile()
	issuing := userCertificateIssuing()
	require.NotNil(t, issuing, "the user certificate must ask cert-manager for a reissue")
	assert.Equal(t, cmmeta.ConditionTrue, issuing.Status)
	assert.Equal(t, "ManagedCARotated", issuing.Reason)

	// cert-manager reissues the certificates of the resource, but not yet the one of the user: the
	// previous client CA is not pruned, the application would lose access.
	for _, name := range clientLeafSecrets {
		if name != user.ClientCertificateSecretName() {
			uxSetCertificateLeaf(t, ctx, kubeClient, rs.Namespace, name, newCA, newCAKey)
		}
	}
	if rs.Annotations == nil {
		rs.Annotations = map[string]string{}
	}
	rs.Annotations[util.PrunePreviousCAAnnotation] = "client"
	require.NoError(t, kubeClient.Update(ctx, rs))

	reconcile()
	assert.Contains(t, clientBundle(), strings.TrimSpace(string(oldCA)), "the previous client CA is kept while the user certificate is signed by it")
	assert.Equal(t, "client", rs.GetAnnotations()[util.PrunePreviousCAAnnotation], "the annotation stays while the prune is refused")

	// cert-manager reissues the user certificate: the previous client CA is pruned.
	uxSetCertificateLeaf(t, ctx, kubeClient, rs.Namespace, user.ClientCertificateSecretName(), newCA, newCAKey)
	reconcile()
	assert.NotContains(t, clientBundle(), strings.TrimSpace(string(oldCA)), "the previous client CA is pruned")
	assert.Contains(t, clientBundle(), strings.TrimSpace(string(newCA)))
	assert.NotContains(t, rs.GetAnnotations(), util.PrunePreviousCAAnnotation)
}

func TestReplicaSetManagedCertificate_WatchesUserClientCertificateSecrets(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}, InternalCluster: util.X509},
	}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, omConnectionFactory.GetConnectionFunc, certs.CertManagerReadiness{Ready: true})
	addKubernetesTlsResources(ctx, kubeClient, rs)

	user := &userv1.MongoDBUser{Spec: userv1.MongoDBUserSpec{
		Database:           "$external",
		MongoDBResourceRef: userv1.MongoDBResourceRef{Name: rs.Name},
		ClientCertificate:  &userv1.ClientCertificate{},
	}}
	user.Name = "myapp"
	user.Namespace = rs.Namespace
	require.NoError(t, kubeClient.Create(ctx, user))

	userSecret := watch.Object{ResourceType: watch.Secret, Resource: kube.ObjectKey(rs.Namespace, "myapp-cert")}
	for range 2 {
		_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		issueManagedCertificates(ctx, t, kubeClient, rs.Namespace)

		assert.Equal(t, []types.NamespacedName{kube.ObjectKey(rs.Namespace, rs.Name)}, reconciler.resourceWatcher.GetWatchedResources()[userSecret])
	}
}
