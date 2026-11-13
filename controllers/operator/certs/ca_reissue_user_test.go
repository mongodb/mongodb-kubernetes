package certs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// uxReissueUserFixture sets up a resource with both categories self-signed, its leaves signed by
// the first CA of each category, the Certificate objects cert-manager would have created, and a
// MongoDBUser whose client certificate (Certificate <user>-client-cert, secret <user>-cert) is
// signed by userCA.
func uxReissueUserFixture(t *testing.T, serverCA, clientCA, userCA *testCA) (*mdbv1.MongoDB, Options, kubernetesClient.Client, *userv1.MongoDBUser) {
	t.Helper()
	c, _ := mock.NewDefaultFakeClient()
	owner, opts, setCAs := managedCATestFixture(t, c)
	opts.InternalClusterSecretName = resName + "-clusterfile"
	setCAs(serverCA.pem, clientCA.pem)
	setCategoryLeaves(t, c, owner, opts, serverCA, clientCA)
	setLeafCertificates(t, c, opts.CertSecretName, opts.InternalClusterSecretName, owner.GetSecurity().AgentClientCertificateSecretName(resName))

	user := appUser(resName)
	uxCreateUser(t, c, user, userCA)
	setLeafCertificates(t, c, user.ClientCertificateName())
	return owner, opts, c, user
}

func TestReissueUserClientCertificatesOnRotatedSelfSignedCA(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	serverCA1, serverCA2 := newTestCA(t, "server-ca-1"), newTestCA(t, "server-ca-2")
	clientCA1, clientCA2 := newTestCA(t, "client-ca-1"), newTestCA(t, "client-ca-2")

	t.Run("a user certificate on the previous client root is reissued", func(t *testing.T) {
		owner, opts, c, user := uxReissueUserFixture(t, serverCA1, clientCA1, clientCA1)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))

		assertCertificateIssuing(t, c, user.ClientCertificateName(), true)
		assertCertificateIssuing(t, c, opts.InternalClusterSecretName, true)
		assertCertificateIssuing(t, c, opts.CertSecretName, false)
	})

	t.Run("a user certificate already on the current root is untouched", func(t *testing.T) {
		owner, opts, c, user := uxReissueUserFixture(t, serverCA1, clientCA1, clientCA2)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))

		assertCertificateIssuing(t, c, user.ClientCertificateName(), false)
		assertCertificateIssuing(t, c, opts.InternalClusterSecretName, true)
	})

	t.Run("a rotated server root does not reissue a user certificate signed by the client CA", func(t *testing.T) {
		owner, opts, c, user := uxReissueUserFixture(t, serverCA1, clientCA1, clientCA1)
		rotateSelfSignedCA(t, c, owner, categoryServer, serverCA2)

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))

		assertCertificateIssuing(t, c, user.ClientCertificateName(), false)
		assertCertificateIssuing(t, c, opts.CertSecretName, true)
	})

	t.Run("a user certificate already Issuing is not updated a second time", func(t *testing.T) {
		owner, opts, c, user := uxReissueUserFixture(t, serverCA1, clientCA1, clientCA1)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))
		before := getCertificate(t, c, user.ClientCertificateName())

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))
		after := getCertificate(t, c, user.ClientCertificateName())

		require.Equal(t, before.ResourceVersion, after.ResourceVersion, "an in-progress reissue must not be triggered again")
	})

	t.Run("a user without a certificate secret or Certificate is tolerated", func(t *testing.T) {
		owner, opts, c, _ := uxReissueUserFixture(t, serverCA1, clientCA1, clientCA1)
		// no secret yet
		uxCreateUser(t, c, uxNamedUser("pending-secret", resName), nil)
		// no Certificate yet
		uxCreateUser(t, c, uxNamedUser("pending-certificate", resName), clientCA1)
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))

		assertCertificateIssuing(t, c, appUser(resName).ClientCertificateName(), true)
	})

	t.Run("the user certificate of another resource is untouched", func(t *testing.T) {
		owner, opts, c, _ := uxReissueUserFixture(t, serverCA1, clientCA1, clientCA2)
		other := uxNamedUser("other-app", "other-mdb")
		uxCreateUser(t, c, other, clientCA1)
		setLeafCertificates(t, c, other.ClientCertificateName())
		rotateSelfSignedCA(t, c, owner, categoryClient, clientCA2)

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))

		assertCertificateIssuing(t, c, other.ClientCertificateName(), false)
	})

	t.Run("the server root signs the user certificates when only applications use x509", func(t *testing.T) {
		// the server CA was rotated to serverCA2 and the member certificate reissued, but the user
		// certificate is still on serverCA1
		owner, opts, c := uxServerSignerFixture(t, serverCA1, serverCA2)
		setLeafCertificates(t, c, opts.CertSecretName)
		user := appUser(resName)
		uxCreateUser(t, c, user, serverCA1)
		setLeafCertificates(t, c, user.ClientCertificateName())

		require.NoError(t, ReissueLeavesOnRotatedSelfSignedCA(ctx, c, owner, []Options{opts}, log))

		assertCertificateIssuing(t, c, user.ClientCertificateName(), true)
		assertCertificateIssuing(t, c, opts.CertSecretName, false)
	})
}
