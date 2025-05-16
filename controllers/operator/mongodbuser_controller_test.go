package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

func TestSettingUserStatus_ToPending_IsFilteredOut(t *testing.T) {
	userInUpdatedPhase := &userv1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: mock.TestNamespace}, Status: userv1.MongoDBUserStatus{Common: status.Common{Phase: status.PhaseUpdated}}}
	userInPendingPhase := &userv1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: mock.TestNamespace}, Status: userv1.MongoDBUserStatus{Common: status.Common{Phase: status.PhasePending}}}

	predicates := watch.PredicatesForUser()
	updateEvent := event.UpdateEvent{
		ObjectOld: userInUpdatedPhase,
		ObjectNew: userInPendingPhase,
	}
	assert.False(t, predicates.UpdateFunc(updateEvent), "changing phase from updated to pending should be filtered out")
}

func TestUserIsAdded_ToAutomationConfig_OnSuccessfulReconciliation(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().EnableAuth().AgentAuthMode("SCRAM").
		SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := omConnectionFactory.GetConnection().ReadAutomationConfig()

	// the automation config should have been updated during reconciliation
	assert.Len(t, ac.Auth.Users, 1, "the MongoDBUser should have been added to the AutomationConfig")

	_, createdUser := ac.Auth.GetUser("my-user", "admin")
	assert.Equal(t, user.Spec.Username, createdUser.Username)
	assert.Equal(t, user.Spec.Database, createdUser.Database)
	assert.Equal(t, len(user.Spec.Roles), len(createdUser.Roles))
}

func TestReconciliationSucceed_OnAddingUser_FromADifferentNamespace(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	user.Spec.MongoDBResourceRef.Namespace = user.Namespace
	// the operator should correctly reconcile if the user resource is in a different namespace than the mongodb resource
	otherNamespace := "userNamespace"
	user.Namespace = otherNamespace

	reconciler, client, _ := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	// we enable auth because we need to explicitly put the password secret in same namespace
	_ = client.Create(ctx, DefaultReplicaSetBuilder().EnableAuth().AgentAuthMode("SCRAM").
		SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)
	// the secret must be in the same namespace as the user, we do not support cross-referencing
	createPasswordSecretInNamespace(ctx, client, user.Spec.PasswordSecretKeyRef, "password", otherNamespace)

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if MongoDBUser and MongoDB resources are in different namespaces")
}

func TestReconciliationSucceed_OnAddingUser_WithNoMongoDBNamespaceSpecified(t *testing.T) {
	ctx := context.Background()
	// DefaultMongoDBUserBuilder doesn't provide a namespace to the MongoDBResourceRef by default
	// The reconciliation should succeed
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, _ := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().EnableAuth().AgentAuthMode("SCRAM").
		SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if MongoDBResourceRef is not provided")
}

func TestUserIsUpdated_IfNonIdentifierFieldIsUpdated_OnSuccessfulReconciliation(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().SetName("my-rs").EnableAuth().AgentAuthMode("SCRAM").
		Build())
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	// remove roles from the same user
	updateUser(ctx, user, client, func(user *userv1.MongoDBUser) {
		user.Spec.Roles = []userv1.Role{}
	})

	actual, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := omConnectionFactory.GetConnection().ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 1, "we should still have a single MongoDBUser, no users should have been deleted")
	_, updatedUser := ac.Auth.GetUser("my-user", "admin")
	assert.Len(t, updatedUser.Roles, 0)
}

func TestUserIsReplaced_IfIdentifierFieldsAreChanged_OnSuccessfulReconciliation(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().SetName("my-rs").EnableAuth().AgentAuthMode("SCRAM").Build())
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	// change the username and database (these are the values used to identify a user)
	updateUser(ctx, user, client, func(user *userv1.MongoDBUser) {
		user.Spec.Username = "changed-name"
		user.Spec.Database = "changed-db"
	})

	actual, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := omConnectionFactory.GetConnection().ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 2, "we should have a new user with the updated fields and a nil value for the deleted user")
	assert.False(t, ac.Auth.HasUser("my-user", "admin"), "the deleted user should no longer be present")
	assert.True(t, containsNil(ac.Auth.Users), "the deleted user should have been assigned a nil value")
	_, updatedUser := ac.Auth.GetUser("changed-name", "changed-db")
	assert.Equal(t, "changed-name", updatedUser.Username, "new user name should be reflected")
	assert.Equal(t, "changed-db", updatedUser.Database, "new database should be reflected")
}

// updateUser applies and updates the changes to the user after getting the most recent version
// from the mocked client
func updateUser(ctx context.Context, user *userv1.MongoDBUser, client client.Client, updateFunc func(*userv1.MongoDBUser)) {
	_ = client.Get(ctx, kube.ObjectKey(user.Namespace, user.Name), user)
	updateFunc(user)
	_ = client.Update(ctx, user)
}

func TestRetriesReconciliation_IfNoPasswordSecretExists(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := defaultUserReconciler(ctx, user)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)

	// No password has been created
	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{RequeueAfter: time.Second * 10}
	assert.Nil(t, err, "should be no error on retry")
	assert.Equal(t, expected, actual, "the reconciliation should be retried as there is no password")

	connection := omConnectionFactory.GetConnection()
	ac, _ := connection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 0, "the MongoDBUser should not have been added to the AutomationConfig")
}

func TestRetriesReconciliation_IfPasswordSecretExists_ButHasNoPassword(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := defaultUserReconciler(ctx, user)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)

	// use the wrong key to store the password
	createPasswordSecret(ctx, client, userv1.SecretKeyRef{Name: user.Spec.PasswordSecretKeyRef.Name, Key: "non-existent-key"}, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{RequeueAfter: time.Second * 10}
	assert.Nil(t, err, "should be no error on retry")
	assert.Equal(t, expected, actual, "the reconciliation should be retried as there is a secret, but the key contains no password")

	connection := omConnectionFactory.GetConnection()
	ac, _ := connection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 0, "the MongoDBUser should not have been added to the AutomationConfig")
}

func TestX509User_DoesntRequirePassword(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetDatabase(authentication.ExternalDB).Build()
	reconciler, client, _ := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigX509Option)

	// initialize resources required for x590 tests
	createMongoDBForUserWithAuth(ctx, client, *user, util.X509)

	createUserControllerConfigMap(ctx, client)

	// No password has been created

	// in order for x509 to be configurable, "util.AutomationConfigX509Option" needs to be enabled on the automation config
	// pre-configure the connection
	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "the reconciliation should be successful as x509 does not require a password")
}

func AssertAuthModeTest(ctx context.Context, t *testing.T, mode mdbv1.AuthMode) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(authentication.ExternalDB).Build()

	reconciler, client, _ := defaultUserReconciler(ctx, user)
	err := client.Create(ctx, DefaultReplicaSetBuilder().EnableAuth().SetAuthModes([]mdbv1.AuthMode{mode}).SetName("my-rs0").Build())
	assert.NoError(t, err)
	createUserControllerConfigMap(ctx, client)
	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	// reconciles if a $external user creation is attempted with no configured backends.
	expected := reconcile.Result{Requeue: false, RequeueAfter: 10 * time.Second}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestExternalAuthUserReconciliation_RequiresExternalAuthConfigured(t *testing.T) {
	ctx := context.Background()
	AssertAuthModeTest(ctx, t, "LDAP")
	AssertAuthModeTest(ctx, t, "X509")
}

func TestScramShaUserReconciliation_CreatesAgentUsers(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	err := client.Create(ctx, DefaultReplicaSetBuilder().AgentAuthMode("SCRAM").EnableAuth().SetName("my-rs").Build())
	assert.NoError(t, err)
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})
	expected, _ := workflow.OK().ReconcileResult()

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig()
	assert.NoError(t, err)

	assert.Len(t, ac.Auth.Users, 1, "users list should contain 1 user just added")
}

func TestMultipleAuthMethod_CreateAgentUsers(t *testing.T) {
	ctx := context.Background()
	t.Run("When SCRAM and X509 auth modes are enabled, and agent mode is SCRAM, 3 users are created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(ctx, t, util.AutomationConfigX509Option, 0, "SCRAM", []mdbv1.AuthMode{"SCRAM", "X509"})

		assert.Equal(t, ac.Auth.AutoUser, "mms-automation-agent")
		assert.Len(t, ac.Auth.Users, 1, "users list should contain a created user")

		expectedUsernames := []string{"mms-backup-agent", "mms-monitoring-agent", "my-user"}
		for _, user := range ac.Auth.Users {
			assert.True(t, stringutil.Contains(expectedUsernames, user.Username))
		}
	})

	t.Run("When X509 and SCRAM auth modes are enabled, and agent mode is X509, 1 user is created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(ctx, t, util.AutomationConfigX509Option, 1, "X509", []mdbv1.AuthMode{"X509", "SCRAM"})
		assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser)
		assert.Len(t, ac.Auth.Users, 1, "users list should contain only 1 user")
		assert.Equal(t, "$external", ac.Auth.Users[0].Database)
		assert.Equal(t, "my-user", ac.Auth.Users[0].Username)
	})

	t.Run("When X509 and SCRAM auth modes are enabled, SCRAM is AgentAuthMode, 3 users are created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(ctx, t, util.AutomationConfigX509Option, 0, "SCRAM", []mdbv1.AuthMode{"X509", "SCRAM"})

		assert.Equal(t, ac.Auth.AutoUser, "mms-automation-agent")
		assert.Len(t, ac.Auth.Users, 1, "users list should contain only one actual user")
		assert.Equal(t, "my-user", ac.Auth.Users[0].Username)
	})

	t.Run("When X509 auth mode is enabled, 1 user will be created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(ctx, t, util.AutomationConfigX509Option, 3, "X509", []mdbv1.AuthMode{"X509"})
		assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser)
		assert.Len(t, ac.Auth.Users, 1, "users list should contain only an actual user")

		expectedUsernames := []string{
			"my-user",
		}
		for _, user := range ac.Auth.Users {
			assert.True(t, stringutil.Contains(expectedUsernames, user.Username))
		}
	})

	t.Run("When LDAP and X509 are enabled, 1 X509 user will be created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(ctx, t, util.AutomationConfigLDAPOption, 3, "X509", []mdbv1.AuthMode{"LDAP", "X509"})
		assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser)
		assert.Len(t, ac.Auth.Users, 1, "users list should contain only an actual user")

		expectedUsernames := []string{
			"my-user",
		}
		for _, user := range ac.Auth.Users {
			assert.True(t, stringutil.Contains(expectedUsernames, user.Username))
		}
	})

	t.Run("When LDAP is enabled, 1 SCRAM agent will be created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(ctx, t, util.AutomationConfigLDAPOption, 0, "LDAP", []mdbv1.AuthMode{"LDAP"})
		assert.Equal(t, "mms-automation-agent", ac.Auth.AutoUser)

		assert.Len(t, ac.Auth.Users, 1, "users list should contain only 1 user")
		assert.Equal(t, "my-user", ac.Auth.Users[0].Username)
		assert.Equal(t, "$external", ac.Auth.Users[0].Database)
	})
}

func TestFinalizerIsAdded_WhenUserIsCreated(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().EnableAuth().AgentAuthMode("SCRAM").
		SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := omConnectionFactory.GetConnection().ReadAutomationConfig()

	// the automation config should have been updated during reconciliation
	assert.Len(t, ac.Auth.Users, 1, "the MongoDBUser should have been added to the AutomationConfig")

	_ = client.Get(ctx, kube.ObjectKey(user.Namespace, user.Name), user)

	assert.Contains(t, user.GetFinalizers(), util.Finalizer)
}

func TestUserReconciler_SavesConnectionStringForMultiShardedCluster(t *testing.T) {
	// Define the details of the member clusters for the sharded cluster setup
	memberClusters := test.NewMemberClusters(
		test.MemberClusterDetails{
			ClusterName:           "member-cluster-1",
			ShardMap:              []int{2, 3},
			NumberOfConfigServers: 2,
			NumberOfMongoses:      2,
		},
		test.MemberClusterDetails{
			ClusterName:           "member-cluster-2",
			ShardMap:              []int{2, 3},
			NumberOfConfigServers: 1,
			NumberOfMongoses:      1,
		},
	)

	ctx := context.Background()

	// Create a sharded cluster with the specified member clusters
	cluster := test.DefaultClusterBuilder().
		WithMultiClusterSetup(memberClusters).
		Build()

	user := DefaultMongoDBUserBuilder().
		SetMongoDBResourceName(cluster.Name).
		Build()

	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(cluster).
		WithObjects(mock.GetDefaultResources()...).
		WithObjects(user).
		Build()

	kubeClient := kubernetesClient.NewClient(fakeClient)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(memberClusters.ClusterNames, omConnectionFactory, true, true)

	reconciler := newMongoDBUserReconciler(ctx, kubeClient, omConnectionFactory.GetConnectionFunc, memberClusterMap)

	_ = kubeClient.Create(ctx, cluster)

	createUserControllerConfigMap(ctx, kubeClient)
	createPasswordSecret(ctx, kubeClient, user.Spec.PasswordSecretKeyRef, "password")

	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})
	require.NoError(t, err)

	secret := &corev1.Secret{}
	err = kubeClient.Get(ctx, kube.ObjectKey(user.Namespace, user.GetConnectionStringSecretName()), secret)
	require.NoError(t, err)

	// Validate connection string contains expected values
	connectionString := string(secret.Data["connectionString.standard"])
	expectedConnectionString := "mongodb://slaney-mongos-0-0-svc.my-namespace.svc.cluster.local," +
		"slaney-mongos-0-1-svc.my-namespace.svc.cluster.local,slaney-mongos-1-0-svc.my-namespace.svc.cluster.local" +
		"/?connectTimeoutMS=20000&serverSelectionTimeoutMS=20000"
	assert.Equal(t, expectedConnectionString, connectionString)
}

func TestFinalizerIsRemoved_WhenUserIsDeleted(t *testing.T) {
	ctx := context.Background()
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client, omConnectionFactory := userReconcilerWithAuthMode(ctx, user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Create(ctx, DefaultReplicaSetBuilder().EnableAuth().AgentAuthMode("SCRAM").
		SetName("my-rs").Build())
	createUserControllerConfigMap(ctx, client)
	createPasswordSecret(ctx, client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := omConnectionFactory.GetConnection().ReadAutomationConfig()

	// the automation config should have been updated during reconciliation
	assert.Len(t, ac.Auth.Users, 1, "the MongoDBUser should have been added to the AutomationConfig")

	_ = client.Delete(ctx, user)

	newResult, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	newExpected, _ := workflow.OK().ReconcileResult()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, newExpected, newResult, "there should be a successful reconciliation if the password is a valid reference")

	assert.Empty(t, user.GetFinalizers())
}

// BuildAuthenticationEnabledReplicaSet returns a AutomationConfig after creating a Replica Set with a set of
// different Authentication values. It should be used to test different combination of authentication modes enabled
// and agent authentication modes.
func BuildAuthenticationEnabledReplicaSet(ctx context.Context, t *testing.T, automationConfigOption string, numAgents int, agentAuthMode string, authModes []mdbv1.AuthMode) *om.AutomationConfig {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(authentication.ExternalDB).Build()

	reconciler, client, omConnectionFactory := defaultUserReconciler(ctx, user)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		_ = connection.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, automationConfigOption)
			return nil
		}, nil)
	})

	builder := DefaultReplicaSetBuilder().EnableAuth().SetAuthModes(authModes).SetName("my-rs")
	if agentAuthMode != "" {
		builder.AgentAuthMode(agentAuthMode)
	}

	err := client.Create(ctx, builder.Build())
	assert.NoError(t, err)
	createUserControllerConfigMap(ctx, client)
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})
	assert.NoError(t, err)

	ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig()
	assert.NoError(t, err)

	return ac
}

// createUserControllerConfigMap creates a configmap with credentials present
func createUserControllerConfigMap(ctx context.Context, client client.Client) {
	_ = client.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: om.TestGroupName, Namespace: mock.TestNamespace},
		Data: map[string]string{
			util.OmBaseUrl:     om.TestURL,
			util.OmOrgId:       om.TestOrgID,
			util.OmProjectName: om.TestGroupName,
			util.OmCredentials: mock.TestCredentialsSecretName,
		},
	})
}

func containsNil(users []*om.MongoDBUser) bool {
	for _, user := range users {
		if user == nil {
			return true
		}
	}
	return false
}

func createPasswordSecret(ctx context.Context, client client.Client, secretRef userv1.SecretKeyRef, password string) {
	createPasswordSecretInNamespace(ctx, client, secretRef, password, mock.TestNamespace)
}

func createPasswordSecretInNamespace(ctx context.Context, client client.Client, secretRef userv1.SecretKeyRef, password string, namespace string) {
	_ = client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef.Name, Namespace: namespace},
		Data: map[string][]byte{
			secretRef.Key: []byte(password),
		},
	})
}

func createMongoDBForUserWithAuth(ctx context.Context, client client.Client, user userv1.MongoDBUser, authModes ...mdbv1.AuthMode) {
	mdbBuilder := DefaultReplicaSetBuilder().SetName(user.Spec.MongoDBResourceRef.Name)

	_ = client.Create(ctx, mdbBuilder.SetAuthModes(authModes).Build())
}

// defaultUserReconciler is the user reconciler used in unit test. It "adds" necessary
// additional K8s objects (st, connection config map and secrets) necessary for reconciliation
func defaultUserReconciler(ctx context.Context, user *userv1.MongoDBUser) (*MongoDBUserReconciler, client.Client, *om.CachedOMConnectionFactory) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(user)
	memberClusterMap := getFakeMultiClusterMap(omConnectionFactory)
	return newMongoDBUserReconciler(ctx, kubeClient, omConnectionFactory.GetConnectionFunc, memberClusterMap), kubeClient, omConnectionFactory
}

func userReconcilerWithAuthMode(ctx context.Context, user *userv1.MongoDBUser, authMode string) (*MongoDBUserReconciler, client.Client, *om.CachedOMConnectionFactory) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(user)
	memberClusterMap := getFakeMultiClusterMap(omConnectionFactory)
	reconciler := newMongoDBUserReconciler(ctx, kubeClient, omConnectionFactory.GetConnectionFunc, memberClusterMap)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		_ = connection.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, authMode)
			// Enabling auth as it's required to be enabled for the user controller to proceed
			ac.Auth.Disabled = false
			return nil
		}, nil)
	})
	return reconciler, kubeClient, omConnectionFactory
}

type MongoDBUserBuilder struct {
	project             string
	passwordRef         userv1.SecretKeyRef
	roles               []userv1.Role
	username            string
	database            string
	resourceName        string
	mongodbResourceName string
	namespace           string
}

func (b *MongoDBUserBuilder) SetPasswordRef(secretName, key string) *MongoDBUserBuilder {
	b.passwordRef = userv1.SecretKeyRef{Name: secretName, Key: key}
	return b
}

func (b *MongoDBUserBuilder) SetMongoDBResourceName(name string) *MongoDBUserBuilder {
	b.mongodbResourceName = name
	return b
}

func (b *MongoDBUserBuilder) SetUsername(username string) *MongoDBUserBuilder {
	b.username = username
	return b
}

func (b *MongoDBUserBuilder) SetNamespace(namespace string) *MongoDBUserBuilder {
	b.namespace = namespace
	return b
}

func (b *MongoDBUserBuilder) SetDatabase(db string) *MongoDBUserBuilder {
	b.database = db
	return b
}

func (b *MongoDBUserBuilder) SetProject(project string) *MongoDBUserBuilder {
	b.project = project
	return b
}

func (b *MongoDBUserBuilder) SetResourceName(resourceName string) *MongoDBUserBuilder {
	b.resourceName = resourceName
	return b
}

func (b *MongoDBUserBuilder) SetRoles(roles []userv1.Role) *MongoDBUserBuilder {
	b.roles = roles
	return b
}

func DefaultMongoDBUserBuilder() *MongoDBUserBuilder {
	return &MongoDBUserBuilder{
		roles: []userv1.Role{{
			RoleName: "role-1",
			Database: "admin",
		}, {
			RoleName: "role-2",
			Database: "admin",
		}, {
			RoleName: "role-3",
			Database: "admin",
		}},
		project: mock.TestProjectConfigMapName,
		passwordRef: userv1.SecretKeyRef{
			Name: "password-secret",
			Key:  "password",
		},
		username:            "my-user",
		database:            "admin",
		mongodbResourceName: mock.TestMongoDBName,
		namespace:           mock.TestNamespace,
	}
}

func (b *MongoDBUserBuilder) Build() *userv1.MongoDBUser {
	if b.roles == nil {
		b.roles = make([]userv1.Role, 0)
	}
	if b.resourceName == "" {
		b.resourceName = b.username
	}

	return &userv1.MongoDBUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.resourceName,
			Namespace: b.namespace,
		},
		Spec: userv1.MongoDBUserSpec{
			Roles:                b.roles,
			PasswordSecretKeyRef: b.passwordRef,
			Username:             b.username,
			Database:             b.database,
			MongoDBResourceRef: userv1.MongoDBResourceRef{
				Name: b.mongodbResourceName,
			},
		},
	}
}
