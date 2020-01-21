package operator

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/api"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOpsManagerReconciler_performValidation(t *testing.T) {
	assert.NoError(t, performValidation(omWithAppDBVersion("4.0.0")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.0.7")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.2.12")))
	assert.NoError(t, performValidation(omWithAppDBVersion("6.0.0")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.2.0-rc1")))
	assert.NoError(t, performValidation(omWithAppDBVersion("4.5.0-ent")))

	assert.Error(t, performValidation(omWithAppDBVersion("3.6.12")))
	assert.Error(t, performValidation(omWithAppDBVersion("3.4.0")))
	assert.Error(t, performValidation(omWithAppDBVersion("3.4.0.0.1.2")))
	assert.Error(t, performValidation(omWithAppDBVersion("foo")))
}

func TestOpsManagerReconciler_prepareOpsManager(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, admin := defaultTestOmReconciler(t, testOm)

	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())

	assert.Equal(t, ok(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", admin.PublicAPIKey)

	// the user "created" in Ops Manager
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "Jane", initializer.currentUsers[0].FirstName)
	assert.Equal(t, "Doe", initializer.currentUsers[0].LastName)
	assert.Equal(t, "pwd", initializer.currentUsers[0].Password)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// One secret was created by the user, another one - by the Operator for the user public key
	// todo the third one is created in `newMockedManager` by default which must be changed
	assert.Len(t, client.secrets, 3)
	expectedSecretData := map[string]string{"user": "jane.doe@g.com", "publicApiKey": "jane.doe@g.com-key"}
	existingSecretData, _ := client.helper().readSecret(objectKey(OperatorNamespace, testOm.APIKeySecretName()))
	assert.Equal(t, expectedSecretData, existingSecretData)
}

// TestOpsManagerReconciler_prepareOpsManagerTwoCalls checks that second call to 'prepareOpsManager' doesn't call
// OM api to create a user as the API secret already exists
func TestOpsManagerReconciler_prepareOpsManagerTwoCalls(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, admin := defaultTestOmReconciler(t, testOm)

	reconciler.prepareOpsManager(testOm, zap.S())

	// let's "update" the user admin secret - this must not affect anything
	client.secrets[objectKey(OperatorNamespace, testOm.APIKeySecretName())].(*corev1.Secret).StringData["Username"] = "this-is-not-expected@g.com"

	// second call is ok - we just don't create the admin user in OM and don't add new secrets
	reconcileStatus, _ := reconciler.prepareOpsManager(testOm, zap.S())
	assert.Equal(t, ok(), reconcileStatus)
	assert.Equal(t, "jane.doe@g.com-key", admin.PublicAPIKey)

	// the call to the api didn't happen
	assert.Equal(t, 1, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	assert.Len(t, client.secrets, 3)
	data, _ := client.helper().readSecret(objectKey(OperatorNamespace, testOm.APIKeySecretName()))
	assert.Equal(t, "jane.doe@g.com", data["user"])
}

// TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser checks that if the public API key secret is removed by the
// user - the Operator will try to create a user again and this will result in UserAlreadyExists error
func TestOpsManagerReconciler_prepareOpsManagerDuplicatedUser(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer, _ := defaultTestOmReconciler(t, testOm)

	reconciler.prepareOpsManager(testOm, zap.S())

	// for some reasons the admin removed the public Api key secret so the call will be done to OM to create a user -
	// it will fail as the user already exists
	delete(client.secrets, objectKey(OperatorNamespace, testOm.APIKeySecretName()))

	reconcileStatus, admin := reconciler.prepareOpsManager(testOm, zap.S())
	assert.IsType(t, &errorStatus{}, reconcileStatus)
	assert.Contains(t, reconcileStatus.(*errorStatus).err.Error(), "USER_ALREADY_EXISTS")
	assert.Nil(t, admin)

	// the call to the api happened, but the user wasn't added
	assert.Equal(t, 2, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)
	assert.Equal(t, "jane.doe@g.com", initializer.currentUsers[0].Username)

	// api secret wasn't created
	assert.Len(t, client.secrets, 2)
	assert.NotContains(t, client.secrets, objectKey(OperatorNamespace, testOm.APIKeySecretName()))
}

func TestOpsManagerGeneratesAppDBPassword_IfNotProvided(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, _, _, _ := defaultTestOmReconciler(t, testOm)

	password, err := reconciler.getAppDBPassword(testOm, zap.S())
	assert.NoError(t, err)
	assert.Len(t, password, 12, "auto generated password should have a size of 12")
}

func TestOpsManagerUsersPassword_SpecifiedInSpec(t *testing.T) {
	testOm := DefaultOpsManagerBuilder().SetAppDBPassword("my-secret", "password").Build()
	reconciler, client, _, _ := defaultTestOmReconciler(t, testOm)

	client.secrets[objectKey(testOm.Namespace, testOm.Spec.AppDB.PasswordSecretKeyRef.Name)] = &corev1.Secret{
		Data: map[string][]byte{
			testOm.Spec.AppDB.PasswordSecretKeyRef.Key: []byte("my-password"), // create the secret with the password
		},
	}

	password, err := reconciler.getAppDBPassword(testOm, zap.S())

	assert.NoError(t, err)
	assert.Equal(t, password, "my-password", "the password specified by the SecretRef should have been returned when specified")
}

// TODO move this test to 'opsmanager_types_test.go' when the builder is moved to 'apis' package
func TestOpsManagerCentralUrl(t *testing.T) {
	assert.Equal(t, "http://testOM-svc.my-namespace.svc.cluster.local:8080",
		DefaultOpsManagerBuilder().Build().CentralURL())
	assert.Equal(t, "http://testOM-svc.my-namespace.svc.some.domain:8080",
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().CentralURL())
}

// TODO move this test to 'opsmanager_types_test.go' when the builder is moved to 'apis' package
func TestOpsManagerBackupDaemonHostName(t *testing.T) {
	assert.Equal(t, "testOM-backup-daemon-0",
		DefaultOpsManagerBuilder().Build().BackupDaemonHostName())
	// The host name doesn't depend on cluster domain
	assert.Equal(t, "testOM-backup-daemon-0",
		DefaultOpsManagerBuilder().SetClusterDomain("some.domain").Build().BackupDaemonHostName())
}

// ******************************************* Helper methods *********************************************************

func defaultTestOmReconciler(t *testing.T, opsManager *mdbv1.MongoDBOpsManager) (*OpsManagerReconciler, *MockedClient,
	*MockedInitializer, *api.MockedOmAdmin) {
	manager := newMockedManager(opsManager)
	// create an admin user secret
	data := map[string]string{"Username": "jane.doe@g.com", "Password": "pwd", "FirstName": "Jane", "LastName": "Doe"}
	_ = manager.client.helper().createSecret(objectKey(opsManager.Namespace, opsManager.Spec.AdminSecret), data,
		map[string]string{}, opsManager)

	initializer := &MockedInitializer{expectedOmURL: opsManager.CentralURL(), t: t}

	// It's important to clean the om state as soon as the reconciler is built!
	admin := api.NewMockedAdmin()
	return newOpsManagerReconciler(manager, om.NewOpsManagerConnection, initializer, api.NewMockedAdminProvider),
		manager.client, initializer, admin
}

func omWithAppDBVersion(version string) *mdbv1.MongoDBOpsManager {
	return DefaultOpsManagerBuilder().SetAppDbVersion(version).Build()
}

// TODO move the builder to 'apis' package as done for mongodb
type OpsManagerBuilder struct {
	om *mdbv1.MongoDBOpsManager
}

func DefaultOpsManagerBuilder() *OpsManagerBuilder {
	spec := mdbv1.MongoDBOpsManagerSpec{
		Version:     "4.2.0",
		AppDB:       *mdbv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	om := &mdbv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "testOM", Namespace: TestNamespace}}
	om.InitDefault()
	return &OpsManagerBuilder{om}
}

func (b *OpsManagerBuilder) SetVersion(version string) *OpsManagerBuilder {
	b.om.Spec.Version = version
	return b
}

func (b *OpsManagerBuilder) SetAppDbVersion(version string) *OpsManagerBuilder {
	b.om.Spec.AppDB.Version = version
	return b
}

func (b *OpsManagerBuilder) SetClusterDomain(clusterDomain string) *OpsManagerBuilder {
	b.om.Spec.ClusterDomain = clusterDomain
	return b
}

func (b *OpsManagerBuilder) SetAppDbMembers(members int) *OpsManagerBuilder {
	b.om.Spec.AppDB.Members = members
	return b
}

func (b *OpsManagerBuilder) SetAppDbFeatureCompatibility(version string) *OpsManagerBuilder {
	b.om.Spec.AppDB.FeatureCompatibilityVersion = &version
	return b
}

func (b *OpsManagerBuilder) SetAppDBPassword(secretName, key string) *OpsManagerBuilder {
	b.om.Spec.AppDB.PasswordSecretKeyRef = &mdbv1.SecretKeyRef{Name: secretName, Key: key}
	return b
}

func (b *OpsManagerBuilder) Build() *mdbv1.MongoDBOpsManager {
	return b.om
}

func (b *OpsManagerBuilder) BuildStatefulSet() (*appsv1.StatefulSet, error) {
	rs := b.om.Spec.AppDB
	return (&KubeHelper{}).NewStatefulSetHelper(b.om).
		SetName(rs.Name()).
		SetService(rs.ServiceName()).
		SetPodVars(&PodVars{}). // TODO remove
		SetClusterName(b.om.ClusterName).
		SetVersion(b.om.Spec.Version).
		BuildStatefulSet()
}

type MockedInitializer struct {
	currentUsers     []*api.User
	expectedAPIError *api.Error
	expectedOmURL    string
	t                *testing.T
	numberOfCalls    int
}

func (o *MockedInitializer) TryCreateUser(omUrl string, user *api.User) (string, error) {
	o.numberOfCalls++
	assert.Equal(o.t, o.expectedOmURL, omUrl)

	if o.expectedAPIError != nil {
		return "", o.expectedAPIError
	}
	// OM logic: any number of users is created. But we cannot of course create the user with the same name
	for _, v := range o.currentUsers {
		if v.Username == user.Username {
			return "", api.NewErrorWithCode(api.UserAlreadyExists)
		}
	}
	o.currentUsers = append(o.currentUsers, user)

	// let's use username as a public api key for simplicity
	return user.Username + "-key", nil
}
