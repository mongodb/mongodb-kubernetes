package certs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// uxCreateUser creates the MongoDBUser and, when ca is not nil, the secret of its client
// certificate, signed by ca.
func uxCreateUser(t *testing.T, c kubernetesClient.Client, user *userv1.MongoDBUser, ca *testCA) {
	t.Helper()
	require.NoError(t, c.Create(context.Background(), user))
	if ca != nil {
		uxSetUserLeaf(t, c, user, ca)
	}
}

// uxSetUserLeaf plays cert-manager writing the client certificate of the user, signed by ca.
func uxSetUserLeaf(t *testing.T, c kubernetesClient.Client, user *userv1.MongoDBUser, ca *testCA) {
	t.Helper()
	setSecret(t, c, user.ClientCertificateSecretName(), map[string][]byte{corev1.TLSCertKey: ca.issueLeaf(t, user.Name)})
}

// uxNamedUser returns a MongoDBUser with a client certificate for the resource refName.
func uxNamedUser(name, refName string) *userv1.MongoDBUser {
	user := appUser(refName)
	user.Name = name
	return user
}

// uxServerSignerFixture is a resource that uses x509 for applications only, so the server CA signs
// the client certificates of its users. Its self-signed server CA was rotated once from serverCA1
// to serverCA2, and the member certificate was reissued by serverCA2.
func uxServerSignerFixture(t *testing.T, serverCA1, serverCA2 *testCA) (*mdbv1.MongoDB, Options, kubernetesClient.Client) {
	t.Helper()
	c, _ := mock.NewDefaultFakeClient()
	owner := newMongoDB(resName, x509ForClientsOnly)
	require.False(t, owner.GetSecurity().RequiresX509ClientCerts())
	opts := Options{ResourceName: resName, CertSecretName: resName + "-cert", Namespace: resNamespace}

	for _, ca := range []*testCA{serverCA1, serverCA2} {
		setSecret(t, c, managedCACertName(owner, categoryServer), map[string][]byte{
			corev1.ServiceAccountRootCAKey: []byte(ca.pem),
			corev1.TLSCertKey:              []byte(ca.pem),
		})
		setSecret(t, c, opts.CertSecretName, map[string][]byte{corev1.TLSCertKey: ca.issueLeaf(t, "member")})
		ensureBundles(t, c, owner, opts, false)
	}

	return owner, opts, c
}

// uxOwnerAdapter is an owner that is not a MongoDB or MongoDBMultiCluster resource, like the
// adapters of the Ops Manager components.
type uxOwnerAdapter struct {
	*mdbv1.MongoDB
}

// TestPrunePreviousCAWithUserClientCertificates covers the client certificates the operator issues
// for MongoDBUsers: the previous CA must stay trusted until they are signed by the current CA.
func TestPrunePreviousCAWithUserClientCertificates(t *testing.T) {
	ctx := context.Background()
	serverCA1, serverCA2 := newTestCA(t, "server-ca-1"), newTestCA(t, "server-ca-2")
	clientCA1, clientCA2 := newTestCA(t, "client-ca-1"), newTestCA(t, "client-ca-2")

	t.Run("the prune is refused while a user certificate is signed by the previous client CA", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		uxCreateUser(t, c, appUser(resName), clientCA1)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "true"}

		status := ensureBundles(t, c, owner, opts, true)
		assert.True(t, status.IsOK(), "a refused prune does not fail the reconcile")

		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey), "the user certificate is signed by the client CA, it does not hold the server CA")
		assert.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey), "the old client CA is kept")
	})

	t.Run("the prune is applied once the user certificate is signed by the current CA", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		user := appUser(resName)
		uxCreateUser(t, c, user, clientCA1)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "client"}
		ensureBundles(t, c, owner, opts, true)
		require.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey))

		uxSetUserLeaf(t, c, user, clientCA2)
		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
	})

	t.Run("users that do not get a certificate for the resource are ignored", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)

		uxCreateUser(t, c, uxNamedUser("other-resource", "other-mdb"), clientCA1)

		otherNamespaceRef := uxNamedUser("other-namespace-ref", resName)
		otherNamespaceRef.Spec.MongoDBResourceRef.Namespace = "other-ns"
		uxCreateUser(t, c, otherNamespaceRef, clientCA1)

		noClientCertificate := uxNamedUser("no-client-certificate", resName)
		noClientCertificate.Spec.ClientCertificate = nil
		uxCreateUser(t, c, noClientCertificate, clientCA1)

		// The user controller does not issue certificates for a user in another namespace.
		otherNamespace := uxNamedUser("other-namespace", resName)
		otherNamespace.Namespace = "other-ns"
		otherNamespace.Spec.MongoDBResourceRef.Namespace = resNamespace
		require.NoError(t, c.Create(ctx, otherNamespace))

		deleted := uxNamedUser("deleted", resName)
		deleted.Finalizers = []string{util.UserFinalizer}
		uxCreateUser(t, c, deleted, clientCA1)
		require.NoError(t, c.Delete(ctx, deleted))

		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "client"}
		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
	})

	t.Run("a user with an explicit reference namespace is matched", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		user := appUser(resName)
		user.Spec.MongoDBResourceRef.Namespace = resNamespace
		uxCreateUser(t, c, user, clientCA1)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "client"}

		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, expectedBundle(clientCA1, clientCA2), latestBundle(t, c, clientCMKey))
	})

	t.Run("a user certificate that is not issued yet is skipped", func(t *testing.T) {
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		uxCreateUser(t, c, appUser(resName), nil)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "client"}

		status := ensureBundles(t, c, owner, opts, true)
		assert.True(t, status.IsOK(), status.Message())

		assert.Equal(t, clientCA2.pem, latestBundle(t, c, clientCMKey))
	})

	t.Run("the server CA signs the user certificates when only applications use x509", func(t *testing.T) {
		owner, opts, c := uxServerSignerFixture(t, serverCA1, serverCA2)
		user := appUser(resName)
		uxCreateUser(t, c, user, serverCA1)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "server"}

		ensureBundles(t, c, owner, opts, false)
		assert.Equal(t, expectedBundle(serverCA1, serverCA2), latestBundle(t, c, serverCMKey), "the old server CA is kept")

		uxSetUserLeaf(t, c, user, serverCA2)
		ensureBundles(t, c, owner, opts, false)
		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey))
	})

	t.Run("the user certificates are checked against the signer category only", func(t *testing.T) {
		// The client CA signs the user certificates, so a user certificate on the previous server CA
		// does not hold the server CA.
		owner, opts, c := rotatedFixture(t, serverCA1, serverCA2, clientCA1, clientCA2)
		uxCreateUser(t, c, appUser(resName), serverCA1)
		owner.Annotations = map[string]string{util.PrunePreviousCAAnnotation: "server"}

		ensureBundles(t, c, owner, opts, true)

		assert.Equal(t, serverCA2.pem, latestBundle(t, c, serverCMKey))
	})
}

func TestUserClientCertificateLeaves(t *testing.T) {
	ctx := context.Background()

	t.Run("the leaves name the Certificate and the secret of the user", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner := newMongoDB(resName, x509Only)
		user := appUser(resName)
		require.NoError(t, c.Create(ctx, user))

		leaves, err := userClientCertificateLeaves(ctx, c, owner, categoryClient)
		require.NoError(t, err)
		assert.Equal(t, []issuedLeaf{{certName: "myapp-client-cert", secretName: "myapp-cert"}}, leaves)

		leaves, err = userClientCertificateLeaves(ctx, c, owner, categoryServer)
		require.NoError(t, err)
		assert.Empty(t, leaves, "the server CA does not sign the user certificates")
	})

	t.Run("the users of a multi-cluster replica set", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetName("mrs").Build()
		mrs.Namespace = resNamespace
		mrs.Spec.Security = &mdbv1.Security{
			ManagedCertificate: &mdbv1.ManagedCertificate{},
			Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.X509}, InternalCluster: util.X509},
		}
		require.NoError(t, c.Create(ctx, appUser("mrs")))
		require.NoError(t, c.Create(ctx, uxNamedUser("other", resName)))

		leaves, err := userClientCertificateLeaves(ctx, c, mrs, categoryClient)
		require.NoError(t, err)
		assert.Equal(t, []issuedLeaf{{certName: "myapp-client-cert", secretName: "myapp-cert"}}, leaves)
	})

	t.Run("a resource without x509 has no user certificates", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
		})
		require.NoError(t, c.Create(ctx, appUser(resName)))

		leaves, err := userClientCertificateLeaves(ctx, c, owner, categoryServer)
		require.NoError(t, err)
		assert.Empty(t, leaves)
	})

	t.Run("an owner that is not a MongoDB or MongoDBMultiCluster resource has no users", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		require.NoError(t, c.Create(ctx, appUser(resName)))

		leaves, err := userClientCertificateLeaves(ctx, c, uxOwnerAdapter{newMongoDB(resName, x509Only)}, categoryClient)
		require.NoError(t, err)
		assert.Empty(t, leaves)
	})
}

func TestUserClientCertificateSecretNames(t *testing.T) {
	ctx := context.Background()

	for name, mutate := range map[string]func(*mdbv1.MongoDB){
		"signed by the client CA": x509Only,
		"signed by the server CA": x509ForClientsOnly,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := mock.NewDefaultFakeClient()
			uxCreateUser(t, c, appUser(resName), nil)
			uxCreateUser(t, c, uxNamedUser("second", resName), nil)
			uxCreateUser(t, c, uxNamedUser("other-resource", "other-mdb"), nil)

			names, err := UserClientCertificateSecretNames(ctx, c, newMongoDB(resName, mutate))
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{"myapp-cert", "second-cert"}, names)
		})
	}

	t.Run("no secrets without managed certificates", func(t *testing.T) {
		c, _ := mock.NewDefaultFakeClient()
		uxCreateUser(t, c, appUser(resName), nil)
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.X509}}
		})

		names, err := UserClientCertificateSecretNames(ctx, c, owner)
		require.NoError(t, err)
		assert.Empty(t, names)
	})

	t.Run("a failed listing of the users is returned", func(t *testing.T) {
		listErr := errors.New("list denied")
		c := newManagedCertTestClientWithInterceptor(t, interceptor.Funcs{
			List: func(ctx context.Context, inner client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return listErr
			},
		})

		_, err := UserClientCertificateSecretNames(ctx, c, newMongoDB(resName, x509Only))
		assert.ErrorIs(t, err, listErr)
	})
}
