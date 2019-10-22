package operator

import (
	"context"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestSettingUserStatus_ToPending_IsFilteredOut(t *testing.T) {
	userInUpdatedPhase := &v1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: TestNamespace}, Status: v1.MongoDBUserStatus{Phase: v1.PhaseUpdated}}
	userInPendingPhase := &v1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: TestNamespace}, Status: v1.MongoDBUserStatus{Phase: v1.PhasePending}}

	predicates := predicatesForUser()
	updateEvent := event.UpdateEvent{
		ObjectOld: userInUpdatedPhase,
		ObjectNew: userInPendingPhase,
	}
	assert.False(t, predicates.UpdateFunc(updateEvent), "changing phase from updated to pending should be filtered out")
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

func createMongoDBForUser(client *MockedClient, user v1.MongoDBUser) {
	mdb := DefaultReplicaSetBuilder().SetName(user.Spec.MongoDBResourceRef.Name).Build()
	_ = client.Update(context.TODO(), mdb)
}

type MongoDBUserBuilder struct {
	roles               []v1.Role
	username            string
	database            string
	resourceName        string
	mongodbResourceName string
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

func (b *MongoDBUserBuilder) SetResourceName(resourceName string) *MongoDBUserBuilder {
	b.resourceName = resourceName
	return b
}

func (b *MongoDBUserBuilder) SetRoles(roles []v1.Role) *MongoDBUserBuilder {
	b.roles = roles
	return b
}

func DefaultMongoDBUserBuilder() *MongoDBUserBuilder {
	return &MongoDBUserBuilder{
		roles: []v1.Role{{
			RoleName: "role-1",
			Database: "admin",
		}, {
			RoleName: "role-2",
			Database: "admin",
		}, {
			RoleName: "role-3",
			Database: "admin",
		}},
		mongodbResourceName: TestMongoDBName,
		username:            "my-user",
		database:            "admin",
	}
}

func (b *MongoDBUserBuilder) Build() *v1.MongoDBUser {
	if b.roles == nil {
		b.roles = make([]v1.Role, 0)
	}
	if b.resourceName == "" {
		b.resourceName = b.username
	}

	return &v1.MongoDBUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.resourceName,
			Namespace: TestNamespace,
		},
		Spec: v1.MongoDBUserSpec{
			Roles:    b.roles,
			Username: b.username,
			Database: b.database,
			MongoDBResourceRef: v1.MongoDBResourceRef{
				Name: b.mongodbResourceName,
			},
		},
	}
}
