package operator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestX509CannotBeEnabled_IfAgentCertsAreNotApproved(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().Build()
	manager := mock.NewManager(rs)

	createConfigMap(t, manager.Client)
	createAgentCSRs(1, manager.Client, certsv1.CertificateDenied)
	addKubernetesTlsResources(manager.Client, rs)
	approveCSRs(manager.Client, rs)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	expectedError := fmt.Sprintf("Agent certs have not yet been approved")
	checkReconcilePending(t, reconciler, rs, expectedError, manager.Client, 10)
}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().Build()
	manager := mock.NewManager(rs)
	createConfigMap(t, manager.Client)
	createAgentCSRs(1, manager.Client, certsv1.CertificateApproved)
	addKubernetesTlsResources(manager.Client, rs)
	approveCSRs(manager.Client, rs)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	checkReconcileSuccessful(t, reconciler, rs, manager.Client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().Build()
	manager := mock.NewManager(rs)
	addKubernetesTlsResources(manager.Client, rs)
	approveCSRs(manager.Client, rs)

	createConfigMap(t, manager.Client)
	createAgentCSRs(1, manager.Client, certsv1.CertificateApproved)
	// enable internal cluster authentication mode
	rs.Spec.Security.ClusterAuthMode = util.X509

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	checkReconcileSuccessful(t, reconciler, rs, manager.Client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ShardedCluster(t *testing.T) {
	scWithTls := DefaultClusterBuilder().EnableTLS().EnableX509().SetName("sc-with-tls").Build()

	reconciler, client := defaultClusterReconciler(scWithTls)
	addKubernetesTlsResources(client, scWithTls)
	createAgentCSRs(1, client, certsv1.CertificateApproved)

	// enable internal cluster authentication mode
	scWithTls.Spec.Security.ClusterAuthMode = util.X509
	checkReconcileSuccessful(t, reconciler, scWithTls, client)
}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ShardedCluster(t *testing.T) {
	scWithTls := DefaultClusterBuilder().EnableTLS().EnableX509().SetName("sc-with-tls").Build()

	reconciler, client := defaultClusterReconciler(scWithTls)
	createAgentCSRs(1, client, certsv1.CertificateApproved)
	addKubernetesTlsResources(client, scWithTls)

	checkReconcileSuccessful(t, reconciler, scWithTls, client)
}

func TestUpdateOmAuthentication_NoAuthenticationEnabled(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).Build()
	processNames := []string{"my-rs-0", "my-rs-1", "my-rs-2"}

	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	r.updateOmAuthentication(conn, processNames, rs, zap.S())

	ac, _ := conn.ReadAutomationConfig()

	assert.True(t, ac.Auth.Disabled, "authentication was not specified to enabled, so it should remain disabled in Ops Manager")
	assert.Len(t, ac.Auth.Users, 0)
}

func TestUpdateOmAuthentication_EnableX509_TlsNotEnabled(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).Build()
	// deployment with existing non-tls non-x509 replica set
	conn := om.NewMockedOmConnection(deployment.CreateFromReplicaSet(rs))

	// configure X509 authentication & tls
	rs.Spec.Security.Authentication.Modes = []string{"X509"}
	rs.Spec.Security.Authentication.Enabled = true
	rs.Spec.Security.TLSConfig.Enabled = true

	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	status, isMultiStageReconciliation := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())

	assert.True(t, status.IsOK(), "configuring both options at once should not result in a failed status")
	assert.True(t, isMultiStageReconciliation, "configuring both tls and x509 at once should result in a multi stage reconciliation")
}

func TestUpdateOmAuthentication_EnableX509_WithTlsAlreadyEnabled(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().Build()
	conn := om.NewMockedOmConnection(deployment.CreateFromReplicaSet(rs))
	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	status, isMultiStageReconciliation := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())

	assert.True(t, status.IsOK(), "configuring x509 when tls has already been enabled should not result in a failed status")
	assert.False(t, isMultiStageReconciliation, "if tls is already enabled, we should be able to configure x509 is a single reconciliation")
}

func TestUpdateOmAuthentication_AuthenticationIsNotConfigured_IfAuthIsNotSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().SetAuthentication(nil).Build()

	rs.Spec.Security.Authentication = nil

	conn := om.NewMockedOmConnection(deployment.CreateFromReplicaSet(rs))
	r := newReplicaSetReconciler(mock.NewManager(rs), func(context *om.OMContext) om.Connection {
		return conn
	})

	status, _ := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())
	assert.True(t, status.IsOK(), "no authentication should have been configured")

	ac, _ := conn.ReadAutomationConfig()

	// authentication has not been touched
	assert.True(t, ac.Auth.Disabled)
	assert.Len(t, ac.Auth.Users, 0)
	assert.Equal(t, "MONGODB-CR", ac.Auth.AutoAuthMechanism)
}

func TestUpdateOmAuthentication_DoesNotDisableAuth_IfAuthIsNotSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	reconciler, client := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection), manager.Client

	addKubernetesTlsResources(client, rs)
	approveAgentCSRs(client, 1)

	checkReconcileSuccessful(t, reconciler, rs, client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	// x509 auth has been enabled
	assert.True(t, ac.Auth.IsEnabled())
	assert.Contains(t, ac.Auth.AutoAuthMechanism, authentication.MongoDBX509)

	rs.Spec.Security.Authentication = nil

	manager = mock.NewManagerSpecificClient(client)
	reconciler = newReplicaSetReconciler(manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	})

	checkReconcileSuccessful(t, reconciler, rs, client)

	ac, _ = om.CurrMockedConnection.ReadAutomationConfig()
	assert.True(t, ac.Auth.IsEnabled())
	assert.Contains(t, ac.Auth.AutoAuthMechanism, authentication.MongoDBX509)
}

func TestCanConfigureAuthenticationDisabled_WithNoModes(t *testing.T) {
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		SetAuthentication(
			&mdbv1.Authentication{
				Enabled: false,
				Modes:   nil,
			}).
		Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	reconciler, client := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection), manager.Client

	addKubernetesTlsResources(client, rs)
	approveAgentCSRs(client, 1)

	checkReconcileSuccessful(t, reconciler, rs, client)
}

func TestUpdateOmAuthentication_EnableX509_FromEmptyDeployment(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())

	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().EnableAuth().EnableX509().Build()
	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	createAgentCSRs(1, r.client, certsv1.CertificateApproved)
	status, isMultiStageReconciliation := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())

	assert.True(t, status.IsOK(), "configuring x509 and tls when there are no processes should not result in a failed status")
	assert.False(t, isMultiStageReconciliation, "if we are enabling tls and x509 at once, this should be done in a single reconciliation")
}

func TestX509AgentUserIsCorrectlyConfigured(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().EnableAuth().EnableX509().Build()
	x509User := DefaultMongoDBUserBuilder().SetDatabase(authentication.ExternalDB).SetMongoDBResourceName("my-rs").Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	err := manager.Client.Create(context.TODO(), x509User)
	assert.NoError(t, err)

	// configure x509/tls resources
	addKubernetesTlsResources(manager.Client, rs)
	createAgentCSRs(1, manager.Client, certsv1.CertificateApproved)
	approveCSRs(manager.Client, rs)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, manager.Client)

	userReconciler := newMongoDBUserReconciler(manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection // use the same connection
	})

	actual, err := userReconciler.Reconcile(context.TODO(), requestFromObject(x509User))
	expected := reconcile.Result{}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Equal(t, ac.Auth.AutoUser, "CN=mms-automation-agent,OU=cloud,O=MongoDB,L=New York,ST=New York,C=US")
}

func TestScramAgentUserIsCorrectlyConfigured(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableAuth().EnableSCRAM().Build()
	scramUser := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	err := manager.Client.Create(context.TODO(), scramUser)
	assert.NoError(t, err)

	userPassword := secret.Builder().
		SetNamespace(scramUser.Namespace).
		SetName(scramUser.Spec.PasswordSecretKeyRef.Name).
		SetField(scramUser.Spec.PasswordSecretKeyRef.Key, "password").
		Build()

	err = manager.Client.Create(context.TODO(), &userPassword)

	assert.NoError(t, err)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, manager.Client)

	userReconciler := newMongoDBUserReconciler(manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection // use the same connection
	})

	actual, err := userReconciler.Reconcile(context.TODO(), requestFromObject(scramUser))
	expected := reconcile.Result{}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
}

func TestScramAgentUser_IsNotOverridden(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableAuth().EnableSCRAM().Build()
	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	reconciler.omConnectionFactory = func(ctx *om.OMContext) om.Connection {
		connection := om.NewEmptyMockedOmConnectionWithAutomationConfigChanges(ctx, func(ac *om.AutomationConfig) {
			ac.Auth.AutoUser = "my-custom-agent-name"
		})
		return connection
	}

	checkReconcileSuccessful(t, reconciler, rs, manager.Client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Equal(t, "my-custom-agent-name", ac.Auth.AutoUser)
}

func TestX509InternalClusterAuthentication_CanBeEnabledWithScram_ReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").
		SetMembers(3).
		EnableAuth().
		EnableSCRAM().
		EnableX509InternalClusterAuth().
		Build()

	manager := mock.NewManager(rs)
	r := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	createConfigMap(t, r.client)
	createAgentCSRs(1, r.client, certsv1.CertificateApproved)
	addKubernetesTlsResources(r.client, rs)

	checkReconcileSuccessful(t, r, rs, manager.Client)

	currConn := om.CurrMockedConnection
	dep, _ := currConn.ReadDeployment()
	for _, p := range dep.ProcessesCopy() {
		assert.Equal(t, p.ClusterAuthMode(), "x509")
	}
}

func TestX509InternalClusterAuthentication_CanBeEnabledWithScram_ShardedCluster(t *testing.T) {
	sc := DefaultClusterBuilder().SetName("my-sc").
		EnableAuth().
		EnableSCRAM().
		EnableX509InternalClusterAuth().
		Build()

	r, manager := newShardedClusterReconcilerFromResource(*sc, om.NewEmptyMockedOmConnection)
	addKubernetesTlsResources(r.client, sc)
	createConfigMap(t, manager.Client)
	createAgentCSRs(1, manager.Client, certsv1.CertificateApproved)
	checkReconcileSuccessful(t, r, sc, manager.Client)

	currConn := om.CurrMockedConnection
	dep, _ := currConn.ReadDeployment()
	for _, p := range dep.ProcessesCopy() {
		assert.Equal(t, p.ClusterAuthMode(), "x509")
	}
}

func TestConfigureLdapDeploymentAuthentication_WithScramAgentAuthentication(t *testing.T) {
	rs := DefaultReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(3).
		SetVersion("4.0.0-ent").
		EnableAuth().
		AgentAuthMode("SCRAM").
		EnableSCRAM().
		EnableLDAP().
		LDAP(
			mdbv1.Ldap{
				BindQueryUser: "bindQueryUser",
				Servers:       []string{"server0:1234", "server1:9876"},
				BindQuerySecretRef: mdbv1.SecretRef{
					Name: "bind-query-password",
				},
			},
		).
		Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	r := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	data := map[string]string{
		"password": "LITZTOd6YiCV8j",
	}
	err := secret.CreateOrUpdate(r.client, secret.Builder().
		SetName("bind-query-password").
		SetNamespace(mock.TestNamespace).
		SetStringData(data).
		Build(),
	)
	assert.NoError(t, err)
	checkReconcileSuccessful(t, r, rs, manager.Client)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)
	assert.Equal(t, "LITZTOd6YiCV8j", ac.Ldap.BindQueryPassword)
	assert.Equal(t, "bindQueryUser", ac.Ldap.BindQueryUser)
	assert.Equal(t, "server0:1234,server1:9876", ac.Ldap.Servers)
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "PLAIN")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
}

func TestConfigureLdapDeploymentAuthentication_WithCustomRole(t *testing.T) {

	customRoles := []mdbv1.MongoDbRole{{
		Db:         "admin",
		Role:       "customRole",
		Roles:      []mdbv1.InheritedRole{{Db: "Admin", Role: "inheritedrole"}},
		Privileges: []mdbv1.Privilege{}},
	}

	rs := DefaultReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(3).
		SetVersion("4.0.0-ent").
		EnableAuth().
		AgentAuthMode("SCRAM").
		EnableSCRAM().
		EnableLDAP().
		LDAP(
			mdbv1.Ldap{
				BindQueryUser: "bindQueryUser",
				Servers:       []string{"server0:1234"},
				BindQuerySecretRef: mdbv1.SecretRef{
					Name: "bind-query-password",
				},
			},
		).
		SetRoles(customRoles).
		Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	r := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	data := map[string]string{
		"password": "LITZTOd6YiCV8j",
	}
	err := secret.CreateOrUpdate(r.client, secret.Builder().
		SetName("bind-query-password").
		SetNamespace(mock.TestNamespace).
		SetStringData(data).
		Build(),
	)
	assert.NoError(t, err)
	checkReconcileSuccessful(t, r, rs, manager.Client)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)
	assert.Equal(t, "server0:1234", ac.Ldap.Servers)

	roles := ac.Deployment["roles"].([]mdbv1.MongoDbRole)
	assert.Len(t, roles, 1)
	assert.Equal(t, customRoles, roles)
}

func TestConfigureLdapDeploymentAuthentication_WithAuthzQueryTemplate_AndUserToDnMapping(t *testing.T) {

	userMapping := `[
                     {
 	               match: "(.+)",
                       substitution: "uid={0},dc=example,dc=org"
                     }
                   ]`
	authzTemplate := "{USER}?memberOf?base"
	rs := DefaultReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(3).
		SetVersion("4.0.0-ent").
		EnableAuth().
		AgentAuthMode("SCRAM").
		EnableSCRAM().
		EnableLDAP().
		LDAP(
			mdbv1.Ldap{
				BindQueryUser: "bindQueryUser",
				Servers:       []string{"server0:0000,server1:1111,server2:2222"},
				BindQuerySecretRef: mdbv1.SecretRef{
					Name: "bind-query-password",
				},
				AuthzQueryTemplate: authzTemplate,
				UserToDNMapping:    userMapping,
			},
		).
		Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()
	r := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	data := map[string]string{
		"password": "LITZTOd6YiCV8j",
	}
	err := secret.CreateOrUpdate(r.client, secret.Builder().
		SetName("bind-query-password").
		SetNamespace(mock.TestNamespace).
		SetStringData(data).
		Build(),
	)
	assert.NoError(t, err)
	checkReconcileSuccessful(t, r, rs, manager.Client)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)
	assert.Equal(t, "server0:0000,server1:1111,server2:2222", ac.Ldap.Servers)

	assert.Equal(t, authzTemplate, ac.Ldap.AuthzQueryTemplate)
	assert.Equal(t, userMapping, ac.Ldap.UserToDnMapping)
}

/*

// TODO: Design a strategy for this particular case. These tests are going to be reworked as part of the
// SCRAM-SHA epic.
//
// https://jira.mongodb.org/browse/CLOUDP-49894
//
func TestX509CannotBeEnabled_WhenThereAreBothTlsAndNonTlsDeployments_ReplicaSet(t *testing.T) {

	rsWithoutTls := DefaultReplicaSetBuilder().SetName("rs-without-tls").Build()
	rsWithTls := DefaultReplicaSetBuilder().EnableTLS().SetName("rs-with-tls").Build()

	// we need to re-use the same connection between different controllers
	connectionFunc := func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	}

	// create a MongoDB resource with TLS enabled
	manager := mock.NewManager(rsWithTls)
	client := manager.Client
	addKubernetesTlsResources(client, rsWithTls)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rsWithTls, client)

	// create a MongoDB resource with TLS disabled
	_ = client.Create(context.TODO(), rsWithoutTls)
	checkReconcileSuccessful(t, reconciler, rsWithoutTls, client)

	// enable x509 authentication at the project level
	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, connectionFunc)
	projectResult, projectErr := projectController.Reconcile(context.TODO(), requestFromObject(cMap))

	expected := reconcileAppDB.Result{RequeueAfter: time.Second * 10}
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult, "the request should have been requeued")

}

func TestX509CannotBeEnabled_WhenThereAreBothTlsAndNonTlsDeployments_ShardedCluster(t *testing.T) {

	scWithoutTls := DefaultClusterBuilder().SetName("sc-without-tls").Build()
	scWithTls := DefaultClusterBuilder().WithTLS().SetName("sc-with-tls").Build()

	// we need to re-use the same connection between different controllers
	connectionFunc := func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	}

	// create a MongoDB resource with TLS enabled
	manager := mock.NewManager(scWithTls)
	client := manager.Client
	addKubernetesTlsResources(client, scWithTls)

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, scWithTls, client)

	// create a MongoDB resource with TLS disabled
	_ = client.Create(context.TODO(), scWithoutTls)
	checkReconcileSuccessful(t, reconciler, scWithoutTls, client)

	// enable x509 authentication at the project level
	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, connectionFunc)
	projectResult, projectErr := projectController.Reconcile(context.TODO(), requestFromObject(cMap))

	expected := reconcileAppDB.Result{RequeueAfter: time.Second * 10}
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult, "the request should have been requeued")

}

*/

// createCSR creates a CSR object which can be set to either CertificateApproved or CertificateDenied
func createCSR(name, ns string, conditionType certsv1.RequestConditionType) certsv1.CertificateSigningRequest {
	return certsv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s.%s", name, ns)},
		Spec: certsv1.CertificateSigningRequestSpec{
			Request: createMockCSRBytes(),
		},
		Status: certsv1.CertificateSigningRequestStatus{
			Conditions: []certsv1.CertificateSigningRequestCondition{
				{Type: conditionType}}}}
}

// TODO: Add this function instead of having all the client/server Secret with certs
// generated in the same function
// func addClientx509Certificates(client *MockedClient, mdb *v1.MongoDB) {
// 	switch mdb.Spec.ResourceType {
// 	case v1.ReplicaSet:
// 		createReplicaSetTLSData(client, mdb)
// 		// TODO: Add Sharded Cluster
// 		// case v1.ShardedCluster:
// 		// 	createShardedClusterTLSData(client, mdb)
// 	}
// }

// addKubernetesTlsResources ensures all the required TLS secrets exist for the given MongoDB resource
func addKubernetesTlsResources(client kubernetesClient.Client, mdb *mdbv1.MongoDB) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mock.TestCredentialsSecretName, Namespace: mock.TestNamespace},
		Data: map[string][]byte{
			"publicApiKey": []byte("someapi"),
			"user":         []byte("someuser"),
		},
	}
	_ = client.Update(context.TODO(), secret)
	switch mdb.Spec.ResourceType {
	case mdbv1.ReplicaSet:
		createReplicaSetTLSData(client, mdb)
	case mdbv1.ShardedCluster:
		createShardedClusterTLSData(client, mdb)
	}
}

// createMockCSRBytes creates a new Certificate Signing Request, signed with a
// fresh private key
func createMockCSRBytes() []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	template := x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		DNSNames: []string{"somehost.com"},
	}
	certRequestBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, priv)
	if err != nil {
		panic(err)
	}

	certRequestPemBytes := &bytes.Buffer{}
	pemBlock := pem.Block{Type: "CERTIFICATE REQUEST", Bytes: certRequestBytes}
	if err := pem.Encode(certRequestPemBytes, &pemBlock); err != nil {
		panic(err)
	}

	return certRequestPemBytes.Bytes()
}

// createMockCertAndKeyBytes generates a random key and certificate and returns
// them as bytes
func createMockCertAndKeyBytes() []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic(err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // cert expires in 10 years
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"somehost.com"},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		panic(err)
	}

	certPemBytes := &bytes.Buffer{}
	if err := pem.Encode(certPemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		panic(err)
	}

	privPemBytes := &bytes.Buffer{}
	if err := pem.Encode(privPemBytes, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		panic(err)
	}

	return append(certPemBytes.Bytes(), privPemBytes.Bytes()...)
}

// createReplicaSetTLSData creates and populates secrets required for a TLS enabled ReplicaSet
func createReplicaSetTLSData(client kubernetesClient.Client, mdb *mdbv1.MongoDB) {
	// Lets create a secret with Certificates and private keys!
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-cert", mdb.Name),
			Namespace: mock.TestNamespace,
		},
	}

	certs := map[string][]byte{}
	clientCerts := map[string][]byte{}
	for i := 0; i < mdb.Spec.Members; i++ {
		pemFile := createMockCertAndKeyBytes()
		certs[fmt.Sprintf("%s-%d-pem", mdb.Name, i)] = pemFile
		clientCerts[fmt.Sprintf("%s-%d-pem", mdb.Name, i)] = pemFile
	}
	secret.Data = certs
	_ = client.Create(context.TODO(), secret)

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       clientCerts,
	})
}

// createShardedClusterTLSData creates and populates all the  secrets needed for a TLS enabled Sharded
// Cluster with internal cluster authentication. Mongos, config server and all shards.
func createShardedClusterTLSData(client kubernetesClient.Client, mdb *mdbv1.MongoDB) {
	// create the secrets for all the shards
	for i := 0; i < mdb.Spec.ShardCount; i++ {
		secretName := fmt.Sprintf("%s-%d-cert", mdb.Name, i)
		shardData := make(map[string][]byte)
		for j := 0; j <= mdb.Spec.MongodsPerShardCount; j++ {
			shardData[fmt.Sprintf("%s-%d-%d-pem", mdb.Name, i, j)] = createMockCertAndKeyBytes()
		}
		_ = client.Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: mock.TestNamespace},
			Data:       shardData,
		})
		_ = client.Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d-clusterfile", mdb.Name, i), Namespace: mock.TestNamespace},
			Data:       shardData,
		})
	}

	// populate with the expected cert and key fields
	mongosData := make(map[string][]byte)
	for i := 0; i < mdb.Spec.MongosCount; i++ {
		mongosData[fmt.Sprintf("%s-mongos-%d-pem", mdb.Name, i)] = createMockCertAndKeyBytes()
	}

	// create the mongos secret
	mongosSecretName := fmt.Sprintf("%s-mongos-cert", mdb.Name)
	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mongosSecretName, Namespace: mock.TestNamespace},
		Data:       mongosData,
	})

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-mongos-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       mongosData,
	})

	// create secret for config server
	configData := make(map[string][]byte)
	for i := 0; i < mdb.Spec.ConfigServerCount; i++ {
		configData[fmt.Sprintf("%s-config-%d-pem", mdb.Name, i)] = createMockCertAndKeyBytes()
	}

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-cert", mdb.Name), Namespace: mock.TestNamespace},
		Data:       configData,
	})

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       configData,
	})

}

// approveAgentCSRs approves all the agent certs needed for x509 authentication
func approveAgentCSRs(client *mock.MockedClient, howMany int) {
	// create the secret the agent certs will exist in
	createAgentCSRs(howMany, client, certsv1.CertificateApproved)
}

// createAgentCSRs creates all the agent CSRs needed for x509 at the specified condition type
func createAgentCSRs(numAgents int, client kubernetesClient.Client, conditionType certsv1.RequestConditionType) {
	if numAgents != 1 && numAgents != 3 {
		return
	}
	// create the secret the agent certs will exist in

	certAuto, _ := ioutil.ReadFile("testdata/certificates/cert_auto")
	certMonitoring, _ := ioutil.ReadFile("testdata/certificates/cert_monitoring")
	certBackup, _ := ioutil.ReadFile("testdata/certificates/cert_backup")

	builder := secret.Builder().
		SetNamespace(mock.TestNamespace).
		SetName(util.AgentSecretName).
		SetField(util.AutomationAgentPemSecretKey, string(certAuto))

	if numAgents == 3 {
		builder.SetField(util.MonitoringAgentPemSecretKey, string(certMonitoring)).
			SetField(util.BackupAgentPemSecretKey, string(certBackup))
	}
	client.CreateSecret(builder.Build())

	addCsrs(client,
		createCSR("mms-automation-agent", mock.TestNamespace, conditionType),
		createCSR("mms-monitoring-agent", mock.TestNamespace, conditionType),
		createCSR("mms-backup-agent", mock.TestNamespace, conditionType),
	)
}

// approveCSRs approves all CSRs related to the given MongoDB resource, this includes TLS and x509 internal cluster authentication CSRs
func approveCSRs(client *mock.MockedClient, mdb *mdbv1.MongoDB) {
	if mdb != nil && mdb.Spec.Security.TLSConfig.Enabled {
		switch mdb.Spec.ResourceType {
		case mdbv1.ReplicaSet:
			for i := 0; i < mdb.Spec.Members; i++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-%d.%s", mdb.Name, i, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-%d.%s-clusterfile", mdb.Name, i, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
			}
		case mdbv1.ShardedCluster:
			// mongos
			for i := 0; i < mdb.Spec.MongosCount; i++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-mongos-0.%s", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-mongos-0.%s-clusterfile", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
			}

			// config server
			for i := 0; i < mdb.Spec.ConfigServerCount; i++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-config-0.%s", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-config-0.%s-clusterfile", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
			}

			// shards
			for shardNum := 0; shardNum < mdb.Spec.ShardCount; shardNum++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-%d.%s", mdb.Name, shardNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-%d.%s-clusterfile", mdb.Name, shardNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
				for mongodNum := 0; mongodNum < mdb.Spec.MongodsPerShardCount; mongodNum++ {
					addCsrs(client,
						createCSR(fmt.Sprintf("%s-%d-%d.%s", mdb.Name, shardNum, mongodNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
						createCSR(fmt.Sprintf("%s-%d-%d.%s-clusterfile", mdb.Name, shardNum, mongodNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					)
				}
			}
		}
	}
}

func createConfigMap(t *testing.T, client kubernetesClient.Client) {

	err := client.CreateConfigMap(configMap())
	assert.NoError(t, err)
}

func TestInvalidPEM_SecretDoesNotContainKey(t *testing.T) {
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	manager := mock.NewManager(rs)
	client := manager.Client

	addKubernetesTlsResources(client, rs)

	//Replace the secret with an empty one
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-cert", rs.Name),
			Namespace: mock.TestNamespace,
		},
	}

	_ = client.Update(context.TODO(), secret)

	err := certs.VerifyCertificatesForStatefulSet(client, fmt.Sprintf("%s-cert", rs.Name), certs.ReplicaSetConfig(*rs))
	for i := 0; i < rs.Spec.Members; i++ {
		expectedErrorMessage := fmt.Sprintf("the secret %s-cert does not contain the expected key %s-%d-pem", rs.Name, rs.Name, i)
		assert.Contains(t, err.Error(), expectedErrorMessage)
	}
}

func TestInvalidPEM_CertificateIsNotComplete(t *testing.T) {
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	manager := mock.NewManager(rs)
	client := manager.Client

	addKubernetesTlsResources(client, rs)

	secret := &corev1.Secret{}

	_ = client.Get(context.TODO(), types.NamespacedName{Name: fmt.Sprintf("%s-cert", rs.Name), Namespace: rs.Namespace}, secret)

	// Delete certificate for member 0 so that certificate is not complete
	secret.Data["temple-0-pem"] = []byte{}

	_ = client.Update(context.TODO(), secret)

	err := certs.VerifyCertificatesForStatefulSet(client, fmt.Sprintf("%s-cert", rs.Name), certs.ReplicaSetConfig(*rs))
	assert.Contains(t, err.Error(), "the certificate is not complete")
}

func Test_NoAdditionalDomainsPresent(t *testing.T) {
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		EnableAuth().
		EnableX509().
		Build()

	// The default secret we create does not contain additional domains so it will not be valid for this RS
	rs.Spec.Security.TLSConfig.AdditionalCertificateDomains = []string{"foo"}

	manager := mock.NewManager(rs)
	client := manager.Client

	addKubernetesTlsResources(client, rs)

	secret := &corev1.Secret{}

	_ = client.Get(context.TODO(), types.NamespacedName{Name: fmt.Sprintf("%s-cert", rs.Name), Namespace: rs.Namespace}, secret)

	err := certs.VerifyCertificatesForStatefulSet(client, fmt.Sprintf("%s-cert", rs.Name), certs.ReplicaSetConfig(*rs))
	for i := 0; i < rs.Spec.Members; i++ {
		expectedErrorMessage := fmt.Sprintf("domain %s-%d.foo is not contained in the list of DNSNames", rs.Name, i)
		assert.Contains(t, err.Error(), expectedErrorMessage)
	}
}
