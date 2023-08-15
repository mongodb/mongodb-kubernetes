package operator

import (
	"context"
	"testing"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
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
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client := userReconcilerWithAuthMode(user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().EnableAuth().AgentAuthMode("SCRAM").
		SetName("my-rs").Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{}

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	connection := om.CurrMockedConnection
	ac, _ := connection.ReadAutomationConfig()

	// the automation config should have been updated during reconciliation
	assert.Len(t, ac.Auth.Users, 1, "the MongoDBUser should have been added to the AutomationConfig")

	_, createdUser := ac.Auth.GetUser("my-user", "admin")
	assert.Equal(t, user.Spec.Username, createdUser.Username)
	assert.Equal(t, user.Spec.Database, createdUser.Database)
	assert.Equal(t, len(user.Spec.Roles), len(createdUser.Roles))
}

func TestUserIsUpdated_IfNonIdentifierFieldIsUpdated_OnSuccessfulReconciliation(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client := userReconcilerWithAuthMode(user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").EnableAuth().AgentAuthMode("SCRAM").
		Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{}

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	// remove roles from the same user
	updateUser(user, client, func(user *userv1.MongoDBUser) {
		user.Spec.Roles = []userv1.Role{}
	})

	actual, err = reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 1, "we should still have a single MongoDBUser, no users should have been deleted")
	_, updatedUser := ac.Auth.GetUser("my-user", "admin")
	assert.Len(t, updatedUser.Roles, 0)
}

func TestUserIsReplaced_IfIdentifierFieldsAreChanged_OnSuccessfulReconciliation(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client := userReconcilerWithAuthMode(user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").EnableAuth().AgentAuthMode("SCRAM").Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{}

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	// change the username and database (these are the values used to identify a user)
	updateUser(user, client, func(user *userv1.MongoDBUser) {
		user.Spec.Username = "changed-name"
		user.Spec.Database = "changed-db"
	})

	actual, err = reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 2, "we should have a new user with the updated fields and a nil value for the deleted user")
	assert.False(t, ac.Auth.HasUser("my-user", "admin"), "the deleted user should no longer be present")
	assert.True(t, containsNil(ac.Auth.Users), "the deleted user should have been assigned a nil value")
	_, updatedUser := ac.Auth.GetUser("changed-name", "changed-db")
	assert.Equal(t, "changed-name", updatedUser.Username, "new user name should be reflected")
	assert.Equal(t, "changed-db", updatedUser.Database, "new database should be reflected")
}

// updateUser applies and updates the changes to the user after getting the most recent version
// from the mocked client
func updateUser(user *userv1.MongoDBUser, client *mock.MockedClient, updateFunc func(*userv1.MongoDBUser)) {
	_ = client.Get(context.TODO(), kube.ObjectKey(user.Namespace, user.Name), user)
	updateFunc(user)
	_ = client.Update(context.TODO(), user)
}

func TestRetriesReconciliation_IfNoPasswordSecretExists(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client := defaultUserReconciler(user)

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)

	// No password has been created
	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{RequeueAfter: time.Second * 10}
	assert.Nil(t, err, "should be no error on retry")
	assert.Equal(t, expected, actual, "the reconciliation should be retried as there is no password")

	connection := om.CurrMockedConnection
	ac, _ := connection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 0, "the MongoDBUser should not have been added to the AutomationConfig")
}

func TestRetriesReconciliation_IfPasswordSecretExists_ButHasNoPassword(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client := defaultUserReconciler(user)

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)

	// use the wrong key to store the password
	createPasswordSecret(client, userv1.SecretKeyRef{Name: user.Spec.PasswordSecretKeyRef.Name, Key: "non-existent-key"}, "password")

	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{RequeueAfter: time.Second * 10}
	assert.Nil(t, err, "should be no error on retry")
	assert.Equal(t, expected, actual, "the reconciliation should be retried as there is a secret, but the key contains no password")

	connection := om.CurrMockedConnection
	ac, _ := connection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 0, "the MongoDBUser should not have been added to the AutomationConfig")
}

func TestX509User_DoesntRequirePassword(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetDatabase(authentication.ExternalDB).Build()
	reconciler, client := userReconcilerWithAuthMode(user, util.AutomationConfigX509Option)

	// initialize resources required for x590 tests
	createMongoDBForUserWithAuth(client, *user, util.X509)

	createUserControllerConfigMap(client)

	// No password has been created

	// in order for x509 to be configurable, "util.AutomationConfigX509Option" needs to be enabled on the automation config
	// pre-configure the connection
	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	expected := reconcile.Result{}

	assert.Nil(t, err, "should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "the reconciliation should be successful as x509 does not require a password")
}

func AssertAuthModeTest(t *testing.T, mode mdbv1.AuthMode) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(authentication.ExternalDB).Build()

	reconciler, client := defaultUserReconciler(user)
	err := client.Update(context.TODO(), DefaultReplicaSetBuilder().EnableAuth().SetAuthModes([]mdbv1.AuthMode{mode}).SetName("my-rs0").Build())
	assert.NoError(t, err)
	createUserControllerConfigMap(client)
	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})

	// reconciles if a $external user creation is attempted with no configured backends.
	expected := reconcile.Result{Requeue: false, RequeueAfter: 10 * time.Second}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestExternalAuthUserReconciliation_RequiresExternalAuthConfigured(t *testing.T) {
	AssertAuthModeTest(t, "LDAP")
	AssertAuthModeTest(t, "X509")
}

func TestScramShaUserReconciliation_CreatesAgentUsers(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	reconciler, client := userReconcilerWithAuthMode(user, util.AutomationConfigScramSha256Option)

	// initialize resources required for the tests
	err := client.Update(context.TODO(), DefaultReplicaSetBuilder().AgentAuthMode("SCRAM").EnableAuth().SetName("my-rs").Build())
	assert.NoError(t, err)
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	actual, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})
	expected := reconcile.Result{}

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)

	assert.Len(t, ac.Auth.Users, 1, "users list should contain 1 user just added")
}

func TestMultipleAuthMethod_CreateAgentUsers(t *testing.T) {
	t.Run("When SCRAM and X509 auth modes are enabled, and agent mode is SCRAM, 3 users are created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(t, util.AutomationConfigX509Option, 0, "SCRAM", []mdbv1.AuthMode{"SCRAM", "X509"})

		assert.Equal(t, ac.Auth.AutoUser, "mms-automation-agent")
		assert.Len(t, ac.Auth.Users, 1, "users list should contain a created user")

		expectedUsernames := []string{"mms-backup-agent", "mms-monitoring-agent", "my-user"}
		for _, user := range ac.Auth.Users {
			assert.True(t, stringutil.Contains(expectedUsernames, user.Username))
		}
	})

	t.Run("When X509 and SCRAM auth modes are enabled, and agent mode is X509, 1 user is created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(t, util.AutomationConfigX509Option, 1, "X509", []mdbv1.AuthMode{"X509", "SCRAM"})
		assert.Equal(t, util.AutomationAgentName, ac.Auth.AutoUser)
		assert.Len(t, ac.Auth.Users, 1, "users list should contain only 1 user")
		assert.Equal(t, "$external", ac.Auth.Users[0].Database)
		assert.Equal(t, "my-user", ac.Auth.Users[0].Username)
	})

	t.Run("When X509 and SCRAM auth modes are enabled, SCRAM is AgentAuthMode, 3 users are created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(t, util.AutomationConfigX509Option, 0, "SCRAM", []mdbv1.AuthMode{"X509", "SCRAM"})

		assert.Equal(t, ac.Auth.AutoUser, "mms-automation-agent")
		assert.Len(t, ac.Auth.Users, 1, "users list should contain only one actual user")
		assert.Equal(t, "my-user", ac.Auth.Users[0].Username)
	})

	t.Run("When X509 auth mode is enabled, 1 user will be created", func(t *testing.T) {
		ac := BuildAuthenticationEnabledReplicaSet(t, util.AutomationConfigX509Option, 3, "X509", []mdbv1.AuthMode{"X509"})
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
		ac := BuildAuthenticationEnabledReplicaSet(t, util.AutomationConfigLDAPOption, 3, "X509", []mdbv1.AuthMode{"LDAP", "X509"})
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
		ac := BuildAuthenticationEnabledReplicaSet(t, util.AutomationConfigLDAPOption, 0, "LDAP", []mdbv1.AuthMode{"LDAP"})
		assert.Equal(t, "mms-automation-agent", ac.Auth.AutoUser)

		assert.Len(t, ac.Auth.Users, 1, "users list should contain only 1 user")
		assert.Equal(t, "my-user", ac.Auth.Users[0].Username)
		assert.Equal(t, "$external", ac.Auth.Users[0].Database)
	})

}

// BuildAuthenticationEnabledReplicaSet returns a AutomationConfig after creating a Replica Set with a set of
// different Authentication values. It should be used to test different combination of authentication modes enabled
// and agent authentication modes.
func BuildAuthenticationEnabledReplicaSet(t *testing.T, automationConfigOption string, numAgents int, agentAuthMode string, authModes []mdbv1.AuthMode) *om.AutomationConfig {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(authentication.ExternalDB).Build()

	reconciler, client := defaultUserReconciler(user)
	reconciler.omConnectionFactory = func(ctx *om.OMContext) om.Connection {
		connection := om.NewEmptyMockedOmConnectionWithAutomationConfigChanges(ctx, func(ac *om.AutomationConfig) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, automationConfigOption)
		})
		return connection
	}

	builder := DefaultReplicaSetBuilder().EnableAuth().SetAuthModes(authModes).SetName("my-rs")
	if agentAuthMode != "" {
		builder.AgentAuthMode(agentAuthMode)
	}

	err := client.Update(context.TODO(), builder.Build())
	assert.NoError(t, err)
	createUserControllerConfigMap(client)
	_, err = reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: kube.ObjectKey(user.Namespace, user.Name)})
	assert.NoError(t, err)

	ac, err := om.CurrMockedConnection.ReadAutomationConfig()
	assert.NoError(t, err)

	return ac
}

// createUserControllerConfigMap creates a configmap with credentials present
func createUserControllerConfigMap(client *mock.MockedClient) {
	_ = client.Update(context.TODO(), &corev1.ConfigMap{
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
func createPasswordSecret(client *mock.MockedClient, secretRef userv1.SecretKeyRef, password string) {
	_ = client.Update(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef.Name, Namespace: mock.TestNamespace},
		Data: map[string][]byte{
			secretRef.Key: []byte(password),
		},
	})
}

func createMongoDBForUserWithAuth(client *mock.MockedClient, user userv1.MongoDBUser, authModes ...mdbv1.AuthMode) {
	mdbBuilder := DefaultReplicaSetBuilder().SetName(user.Spec.MongoDBResourceRef.Name)

	_ = client.Update(context.TODO(), mdbBuilder.SetAuthModes(authModes).Build())
}

// defaultUserReconciler is the user reconciler used in unit test. It "adds" necessary
// additional K8s objects (st, connection config map and secrets) necessary for reconciliation
func defaultUserReconciler(user *userv1.MongoDBUser) (*MongoDBUserReconciler, *mock.MockedClient) {
	manager := mock.NewManager(user)
	manager.Client.AddDefaultMdbConfigResources()
	memberClusterMap := getFakeMultiClusterMap()
	return newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection, memberClusterMap), manager.Client
}

func userReconcilerWithAuthMode(user *userv1.MongoDBUser, authMode string) (*MongoDBUserReconciler, *mock.MockedClient) {
	manager := mock.NewManager(user)
	manager.Client.AddDefaultMdbConfigResources()
	memberClusterMap := getFakeMultiClusterMap()
	reconciler := newMongoDBUserReconciler(manager, func(context *om.OMContext) om.Connection {
		connection := om.NewEmptyMockedOmConnectionWithAutomationConfigChanges(context, func(ac *om.AutomationConfig) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, authMode)
			// Enabling auth as it's required to be enabled for the user controller to proceed
			ac.Auth.Disabled = false
		})
		return connection
	}, memberClusterMap)
	return reconciler, manager.Client
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
