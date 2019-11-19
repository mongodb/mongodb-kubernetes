package operator

import (
	"context"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestSettingUserStatus_ToPending_IsFilteredOut(t *testing.T) {
	userInUpdatedPhase := &mdbv1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: TestNamespace}, Status: mdbv1.MongoDBUserStatus{Phase: mdbv1.PhaseUpdated}}
	userInPendingPhase := &mdbv1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: TestNamespace}, Status: mdbv1.MongoDBUserStatus{Phase: mdbv1.PhasePending}}

	predicates := predicatesForUser()
	updateEvent := event.UpdateEvent{
		ObjectOld: userInUpdatedPhase,
		ObjectNew: userInPendingPhase,
	}
	assert.False(t, predicates.UpdateFunc(updateEvent), "changing phase from updated to pending should be filtered out")
}

func TestUserIsAdded_ToAutomationConfig_OnSuccessfulReconciliation(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	reconciler := newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection)
	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := success()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	connection := om.CurrMockedConnection
	ac, _ := connection.ReadAutomationConfig()

	// the automation config should have been updated during reconciliation
	assert.Len(t, ac.Auth.Users, 3, "the MongoDBUser and agent users should have been added to the AutomationConfig")

	_, createdUser := ac.Auth.GetUser("my-user", "admin")
	assert.Equal(t, user.Spec.Username, createdUser.Username)
	assert.Equal(t, user.Spec.Database, createdUser.Database)
	assert.Equal(t, len(user.Spec.Roles), len(createdUser.Roles))
}

func TestUserIsUpdated_IfNonIdentifierFieldIsUpdated_OnSuccessfulReconciliation(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	reconciler := newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection)
	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := success()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	// remove roles from the same user
	updateUser(user, client, func(user *mdbv1.MongoDBUser) {
		user.Spec.Roles = []mdbv1.Role{}
	})

	actual, err = reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 3, "we should still have a single MongoDBUser and the 2 agent users, no users should have been deleted")
	_, updatedUser := ac.Auth.GetUser("my-user", "admin")
	assert.Len(t, updatedUser.Roles, 0)
}

func TestUserIsReplaced_IfIdentifierFieldsAreChanged_OnSuccessfulReconciliation(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	reconciler := newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection)
	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := success()

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	// change the username and database (these are the values used to identify a user)
	updateUser(user, client, func(user *mdbv1.MongoDBUser) {
		user.Spec.Username = "changed-name"
		user.Spec.Database = "changed-db"
	})

	actual, err = reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	assert.Nil(t, err, "there should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "there should be a successful reconciliation if the password is a valid reference")

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 4, "we should have a new user with the updated fields and a nil value for the deleted user, and 2 agent users")
	assert.False(t, ac.Auth.HasUser("my-user", "admin"), "the deleted user should no longer be present")
	assert.True(t, containsNil(ac.Auth.Users), "the deleted user should have been assigned a nil value")
	_, updatedUser := ac.Auth.GetUser("changed-name", "changed-db")
	assert.Equal(t, "changed-name", updatedUser.Username, "new user name should be reflected")
	assert.Equal(t, "changed-db", updatedUser.Database, "new database should be reflected")
}

// updateUser applies and updates the changes to the user after getting the most recent version
// from the mocked client
func updateUser(user *mdbv1.MongoDBUser, client *MockedClient, updateFunc func(*mdbv1.MongoDBUser)) {
	_ = client.Get(context.TODO(), objectKey(user.Namespace, user.Name), user)
	updateFunc(user)
	_ = client.Update(context.TODO(), user)
}

func TestRetriesReconciliation_IfNoPasswordSecretExists(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)

	// No password has been created
	reconciler := newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection)
	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := retry()
	assert.Nil(t, err, "should be no error on retry")
	assert.Equal(t, expected, actual, "the reconciliation should be retried as there is no password")

	connection := om.CurrMockedConnection
	ac, _ := connection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 0, "the MongoDBUser should not have been added to the AutomationConfig")
}

func TestRetriesReconciliation_IfPasswordSecretExists_ButHasNoPassword(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)

	// use the wrong key to store the password
	createPasswordSecret(client, mdbv1.SecretKeyRef{Name: user.Spec.PasswordSecretKeyRef.Name, Key: "non-existent-key"}, "password")

	reconciler := newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection)
	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := retry()
	assert.Nil(t, err, "should be no error on retry")
	assert.Equal(t, expected, actual, "the reconciliation should be retried as there is a secret, but the key contains no password")

	connection := om.CurrMockedConnection
	ac, _ := connection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 0, "the MongoDBUser should not have been added to the AutomationConfig")
}

func TestX509User_DoesntRequirePassword(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetDatabase(util.X509Db).Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for x590 tests
	createMongoDBForUser(client, *user)

	createX509UserControllerConfigMap(client)
	approveAgentCSRs(client) // pre-approved agent CSRs for x509 authentication

	// No password has been created

	// in order for x509 to be configurable, "util.AutomationConfigX509Option" needs to be enabled on the automation config
	// pre-configure the connection
	reconciler := newMongoDBUserReconciler(manager, func(ctx *om.OMContext) om.Connection {
		connection := om.NewEmptyMockedOmConnectionWithAutomationConfigChanges(ctx, func(ac *om.AutomationConfig) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option)
		})
		return connection
	})

	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := success()

	assert.Nil(t, err, "should be no error on successful reconciliation")
	assert.Equal(t, expected, actual, "the reconciliation should be successful as x509 does not require a password")
}

func TestScramShaUserReconciliation_CreatesAgentUsers(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()
	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for the tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().SetName("my-rs").Build())
	createUserControllerConfigMap(client)
	createPasswordSecret(client, user.Spec.PasswordSecretKeyRef, "password")

	reconciler := newMongoDBUserReconciler(manager, om.NewEmptyMockedOmConnection)
	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})
	expected, _ := success()

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 3, "users list should contain 1 user just added and 2 agent users")
}

func TestX509UserReconciliation_CreatesAgentUsers(t *testing.T) {
	user := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").SetDatabase(util.X509Db).Build()

	manager := newMockedManager(user)
	client := manager.client

	// initialize resources required for x590 tests
	_ = client.Update(context.TODO(), DefaultReplicaSetBuilder().EnableAuth().SetAuthModes([]string{"X509"}).SetName("my-rs").Build())
	createX509UserControllerConfigMap(client)
	approveAgentCSRs(client) // pre-approved agent CSRs for x509 authentication

	reconciler := newMongoDBUserReconciler(manager, func(ctx *om.OMContext) om.Connection {
		connection := om.NewEmptyMockedOmConnectionWithAutomationConfigChanges(ctx, func(ac *om.AutomationConfig) {
			ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option)
		})
		return connection
	})

	actual, err := reconciler.Reconcile(reconcile.Request{NamespacedName: objectKey(user.Namespace, user.Name)})

	expected, _ := success()

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()

	assert.Len(t, ac.Auth.Users, 3, "users list should contain 1 user just added and 2 agent users")
}

// createUserControllerConfigMap creates a configmap with credentials present
func createUserControllerConfigMap(client *MockedClient) {
	_ = client.Update(context.TODO(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: om.TestGroupName, Namespace: TestNamespace},
		Data: map[string]string{
			util.OmBaseUrl:     om.TestURL,
			util.OmProjectName: om.TestGroupName,
			util.OmCredentials: TestCredentialsSecretName,
		},
	})
}

func createX509UserControllerConfigMap(client *MockedClient) {
	_ = client.Update(context.TODO(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: om.TestGroupName, Namespace: TestNamespace},
		Data: map[string]string{
			util.OmBaseUrl:     om.TestURL,
			util.OmProjectName: om.TestGroupName,
			util.OmCredentials: TestCredentialsSecretName,
			util.OmAuthMode:    util.X509,
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
func createPasswordSecret(client *MockedClient, secretRef mdbv1.SecretKeyRef, password string) {
	_ = client.Update(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretRef.Name, Namespace: TestNamespace},
		Data: map[string][]byte{
			secretRef.Key: []byte(password),
		},
	})
}

func createMongoDBForUser(client *MockedClient, user mdbv1.MongoDBUser) {
	mdb := DefaultReplicaSetBuilder().SetName(user.Spec.MongoDBResourceRef.Name).Build()
	_ = client.Update(context.TODO(), mdb)
}

type MongoDBUserBuilder struct {
	project             string
	passwordRef         mdbv1.SecretKeyRef
	roles               []mdbv1.Role
	username            string
	database            string
	resourceName        string
	mongodbResourceName string
}

func (b *MongoDBUserBuilder) SetPasswordRef(secretName, key string) *MongoDBUserBuilder {
	b.passwordRef = mdbv1.SecretKeyRef{Name: secretName, Key: key}
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

func (b *MongoDBUserBuilder) SetRoles(roles []mdbv1.Role) *MongoDBUserBuilder {
	b.roles = roles
	return b
}

func DefaultMongoDBUserBuilder() *MongoDBUserBuilder {
	return &MongoDBUserBuilder{
		roles: []mdbv1.Role{{
			RoleName: "role-1",
			Database: "admin",
		}, {
			RoleName: "role-2",
			Database: "admin",
		}, {
			RoleName: "role-3",
			Database: "admin",
		}},
		project: TestProjectConfigMapName,
		passwordRef: mdbv1.SecretKeyRef{
			Name: "password-secret",
			Key:  "password",
		},
		username:            "my-user",
		database:            "admin",
		mongodbResourceName: TestMongoDBName,
	}
}

func (b *MongoDBUserBuilder) Build() *mdbv1.MongoDBUser {
	if b.roles == nil {
		b.roles = make([]mdbv1.Role, 0)
	}
	if b.resourceName == "" {
		b.resourceName = b.username
	}

	return &mdbv1.MongoDBUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.resourceName,
			Namespace: TestNamespace,
		},
		Spec: mdbv1.MongoDBUserSpec{
			Roles:                b.roles,
			Project:              b.project,
			PasswordSecretKeyRef: b.passwordRef,
			Username:             b.username,
			Database:             b.database,
			MongoDBResourceRef: mdbv1.MongoDBResourceRef{
				Name: b.mongodbResourceName,
			},
		},
	}
}
