package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const clientCertUsername = "CN=my-user,OU=" + mock.TestNamespace + ",O=my-rs-app-client"

// newClientCertUser returns a MongoDBUser that authenticates with a certificate: spec.clientCertificate is
// set, spec.db is $external, and spec.username is left empty for the operator to fill in.
func newClientCertUser() *userv1.MongoDBUser {
	u := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(authentication.ExternalDB).Build()
	u.Spec.Username = ""
	u.Spec.PasswordSecretKeyRef = userv1.SecretKeyRef{}
	u.Spec.ClientCertificate = &userv1.ClientCertificate{}
	return u
}

// createManagedX509ReplicaSet creates the MongoDB resource the user points at, with operator-managed
// certificates and x509 as the only authentication mode.
func createManagedX509ReplicaSet(ctx context.Context, t *testing.T, c client.Client, u *userv1.MongoDBUser) {
	mdb := DefaultReplicaSetBuilder().SetName(u.Spec.MongoDBResourceRef.Name).EnableX509().EnableX509InternalClusterAuth().Build()
	mdb.Namespace = u.Namespace
	mdb.Spec.Security.TLSConfig = &mdbv1.TLSConfig{Enabled: true}
	mdb.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	require.NoError(t, c.Create(ctx, mdb))
}

// markClientCertReady sets the user's client Certificate to Ready, the way cert-manager would.
func markClientCertReady(ctx context.Context, t *testing.T, c client.Client, u *userv1.MongoDBUser) {
	cert := &certmanagerv1.Certificate{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name+"-client-cert"), cert))
	cert.Status.Conditions = []certmanagerv1.CertificateCondition{{
		Type: certmanagerv1.CertificateConditionReady, Status: cmmeta.ConditionTrue, ObservedGeneration: cert.Generation,
	}}
	// The fake client has no status subresource for Certificate, so a plain update writes the status.
	require.NoError(t, c.Update(ctx, cert))
}

// reconcileUser reconciles the MongoDBUser once, and fails the test if the reconcile itself returns an error.
func reconcileUser(ctx context.Context, t *testing.T, r *MongoDBUserReconciler, u *userv1.MongoDBUser) reconcile.Result {
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(u.Namespace, u.Name)})
	require.NoError(t, err)
	return res
}

// The whole flow. The operator creates the Certificate, owned by the user and signed by the client CA, and
// the MongoDBUser stays Pending while cert-manager issues it. Once it is ready, the operator adds the user to
// the MongoDB deployment, naming it after the certificate subject, and the status reports the Secret with the
// certificate and the ConfigMap with the server CA.
func TestClientCertificateUser_IsRegisteredUnderTheCertificateSubject(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)

	// First reconcile creates the Certificate and waits for cert-manager.
	reconcileUser(ctx, t, reconciler, u)
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	assert.Equal(t, status.PhasePending, u.Status.Phase, "pending until cert-manager issues the certificate")

	cert := &certmanagerv1.Certificate{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, "my-user-client-cert"), cert))
	assert.Equal(t, "my-user-cert", cert.Spec.SecretName)
	assert.Equal(t, "my-rs-client-ca-issuer", cert.Spec.IssuerRef.Name)
	require.Len(t, cert.OwnerReferences, 1, "the certificate is owned by the user, so it is deleted with it")
	assert.Equal(t, "MongoDBUser", cert.OwnerReferences[0].Kind)
	assert.Equal(t, u.Name, cert.OwnerReferences[0].Name)

	// Once issued, the user is registered under the certificate subject.
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	assert.Equal(t, status.PhaseUpdated, u.Status.Phase)
	assert.Equal(t, clientCertUsername, u.Status.Username)
	assert.Empty(t, u.Spec.Username, "the operator never writes the derived username into spec")
	require.NotNil(t, u.Status.ClientCertificate)
	assert.Equal(t, "my-user-cert", u.Status.ClientCertificate.SecretName)
	assert.Equal(t, "my-rs-managed-server-ca-bundle", u.Status.ClientCertificate.ServerCAConfigMapName)

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, registered := ac.Auth.GetUser(clientCertUsername, authentication.ExternalDB)
	require.NotNil(t, registered, "the user is in the automation config under the subject")

	// The connection string secret uses the certificate subject as the username. Where the certificate
	// and the server CA are is reported only in the status, not repeated here.
	secret := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.GetConnectionStringSecretName()), secret))
	assert.Equal(t, clientCertUsername, string(secret.Data["username"]))
	assert.NotContains(t, secret.Data, "password", "an x509 user has no password")
	assert.NotContains(t, secret.Data, "clientCertificateSecret")
	assert.NotContains(t, secret.Data, "serverCAConfigMap")
}

// A spec.username that matches the certificate subject is accepted, and the status records that same name.
func TestClientCertificateUser_MatchingUsernameIsAccepted(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	u.Spec.Username = clientCertUsername
	reconciler, c, _ := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	assert.Equal(t, status.PhaseUpdated, u.Status.Phase)
	assert.Equal(t, clientCertUsername, u.Status.Username)
}

// A spec.username that does not match the certificate subject fails, with a message giving the value to use.
// The operator adds nothing to the MongoDB deployment and creates no certificate.
func TestClientCertificateUser_MismatchedUsernameIsRejected(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	u.Spec.Username = "CN=someone-else"
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	assert.Equal(t, status.PhaseFailed, u.Status.Phase)
	assert.Contains(t, u.Status.Message, "does not match the client certificate subject")
	assert.Contains(t, u.Status.Message, clientCertUsername, "the message gives the exact value to use")

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	assert.Empty(t, ac.Auth.Users, "no user is registered when the username does not match")
	assert.True(t, apiErrors.IsNotFound(c.Get(ctx, kube.ObjectKey(u.Namespace, "my-user-client-cert"), &certmanagerv1.Certificate{})), "no certificate is issued")
}

// x509 is not one of the authentication modes, so an application could not log in with a certificate, even
// though internal cluster auth makes the operator create a client CA. The MongoDBUser fails with a message
// saying so.
func TestClientCertificateUser_RejectedWhenX509IsNotAnAuthMode(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	reconciler, c, _ := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	mdb := DefaultReplicaSetBuilder().SetName("my-rs").EnableAuth().SetAuthModes([]mdbv1.AuthMode{util.SCRAM}).Build()
	mdb.Namespace = u.Namespace
	mdb.Spec.Security.TLSConfig = &mdbv1.TLSConfig{Enabled: true}
	mdb.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	mdb.Spec.Security.Authentication.InternalCluster = util.X509
	require.NoError(t, c.Create(ctx, mdb))
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	assert.Equal(t, status.PhaseFailed, u.Status.Phase)
	assert.Contains(t, u.Status.Message, "does not use x509 authentication")
}

// Deleting the MongoDBUser resource removes the matching user from the MongoDB deployment. The operator
// removes it by the name it was registered with, which is the certificate subject.
func TestClientCertificateUser_IsRemovedUnderTheSubjectOnDelete(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, registered := ac.Auth.GetUser(clientCertUsername, authentication.ExternalDB)
	require.NotNil(t, registered)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	require.NoError(t, c.Delete(ctx, u))
	reconcileUser(ctx, t, reconciler, u)

	ac, err = omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, stillThere := ac.Auth.GetUser(clientCertUsername, authentication.ExternalDB)
	assert.Nil(t, stillThere, "the x509 user is removed from MongoDB under its subject, not under an empty name")
}

// Workflow for a MongoDBUser without spec.clientCertificate is unchanged: the operator adds it to the MongoDB deployment
// using spec.username as its name, and creates no certificate for it.
func TestUserWithoutClientCertificate_StillUsesSpecUsername(t *testing.T) {
	ctx := context.Background()
	u := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(authentication.ExternalDB).Build()
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createMongoDBForUserWithAuth(ctx, c, *u, util.X509)
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, registered := ac.Auth.GetUser(u.Spec.Username, authentication.ExternalDB)
	assert.NotNil(t, registered, "a user without clientCertificate is registered under spec.username, as before")
	assert.True(t, apiErrors.IsNotFound(c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name+"-client-cert"), &certmanagerv1.Certificate{})), "no certificate is issued")
}

// Ops Manager connects to backup databases with a username and password, so a MongoDBUser that authenticates
// with a certificate is rejected as a backup store user. A SCRAM user is allowed.
func TestValidateBackupStoreUser(t *testing.T) {
	key := kube.ObjectKey(mock.TestNamespace, "my-user")

	scram := DefaultMongoDBUserBuilder().Build()
	assert.True(t, validateBackupStoreUser(scram, key).IsOK(), "a SCRAM user can be a backup store user")

	x509 := newClientCertUser()
	st := validateBackupStoreUser(x509, key)
	require.False(t, st.IsOK())
	assert.Contains(t, st.Message(), "support only SCRAM users")
}

// Deleting the MongoDBUser resource still removes the right user when the certificate subject can no longer
// be worked out, here because the database stopped using x509. The operator removes the name recorded in
// status.username, which is the name it registered earlier.
func TestClientCertificateUser_IsRemovedOnDelete_EvenWhenTheSubjectCannotBeDerived(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)

	mdb := &mdbv1.MongoDB{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, "my-rs"), mdb))
	mdb.Spec.Security.Authentication.Modes = []mdbv1.AuthMode{util.SCRAM}
	require.NoError(t, c.Update(ctx, mdb))

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	require.NoError(t, c.Delete(ctx, u))
	reconcileUser(ctx, t, reconciler, u)

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, stillThere := ac.Auth.GetUser(clientCertUsername, authentication.ExternalDB)
	assert.Nil(t, stillThere, "the registered user is removed even though the subject can no longer be derived")
}

// Changing the certificate subject changes the name the user is registered with: the operator removes the old
// user from the MongoDB deployment, adds the new one, and updates the Certificate to the new subject.
func TestClientCertificateUser_ChangingTheSubjectReplacesTheUser(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)

	reconcileUser(ctx, t, reconciler, u)
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	u.Spec.ClientCertificate.Subject = &userv1.ClientCertificateSubject{Organizations: []string{"apps"}}
	require.NoError(t, c.Update(ctx, u))
	reconcileUser(ctx, t, reconciler, u)
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)

	newUsername := "CN=my-user,OU=" + mock.TestNamespace + ",O=apps"
	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, oldUser := ac.Auth.GetUser(clientCertUsername, authentication.ExternalDB)
	_, newUser := ac.Auth.GetUser(newUsername, authentication.ExternalDB)
	assert.Nil(t, oldUser, "the user under the old subject is removed")
	assert.NotNil(t, newUser, "the user is registered under the new subject")

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	assert.Equal(t, newUsername, u.Status.Username)

	cert := &certmanagerv1.Certificate{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.ClientCertificateName()), cert))
	assert.Equal(t, []string{"apps"}, cert.Spec.Subject.Organizations, "the certificate follows the new subject")
}

// A MongoDBUser whose spec.username no longer matches the certificate subject can still be deleted, and the
// user the operator registered earlier is removed from the MongoDB deployment.
func TestClientCertificateUser_MismatchedUsernameDoesNotBlockDelete(t *testing.T) {
	ctx := context.Background()
	u := newClientCertUser()
	reconciler, c, omConnectionFactory := userReconcilerWithAuthMode(ctx, u, util.AutomationConfigX509Option)
	createManagedX509ReplicaSet(ctx, t, c, u)
	createUserControllerConfigMap(ctx, c)
	reconcileUser(ctx, t, reconciler, u)
	markClientCertReady(ctx, t, c, u)
	reconcileUser(ctx, t, reconciler, u)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	u.Spec.Username = "CN=wrong"
	require.NoError(t, c.Update(ctx, u))
	reconcileUser(ctx, t, reconciler, u)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), u))
	require.NoError(t, c.Delete(ctx, u))
	reconcileUser(ctx, t, reconciler, u)

	assert.True(t, apiErrors.IsNotFound(c.Get(ctx, kube.ObjectKey(u.Namespace, u.Name), &userv1.MongoDBUser{})), "the finalizer is removed and the user is gone")
	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig(ctx)
	require.NoError(t, err)
	_, stillThere := ac.Auth.GetUser(clientCertUsername, authentication.ExternalDB)
	assert.Nil(t, stillThere, "the registered x509 user is removed")
}
