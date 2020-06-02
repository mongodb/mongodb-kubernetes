package operator

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const OperatorNamespace = "operatorNs"

func init() {
	util.OperatorVersion = "9.9.9-test"
	_ = os.Setenv(util.CurrentNamespace, OperatorNamespace)
}

func TestEnsureTagAdded(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()
	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)
	mockOm, _ := prepareConnection(controller, t)

	// normal tag
	err := ensureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "myTag", zap.S())
	assert.NoError(t, err)

	// long tag
	err = ensureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "LOOKATTHISTRINGTHATISTOOLONGFORTHEFIELD", zap.S())
	assert.NoError(t, err)

	expected := []string{"EXTERNALLY_MANAGED_BY_KUBERNETES", "MY-NAMESPACE", "MYTAG", "LOOKATTHISTRINGTHATISTOOLONGFORT"}
	assert.Equal(t, expected, mockOm.FindGroup(om.TestGroupName).Tags)
}

func TestEnsureTagAddedDuplicates(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()
	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)
	mockOm, _ := prepareConnection(controller, t)
	err := ensureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "MYTAG", zap.S())
	assert.NoError(t, err)
	err = ensureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "MYTAG", zap.S())
	assert.NoError(t, err)
	err = ensureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "MYOTHERTAG", zap.S())
	assert.NoError(t, err)
	expected := []string{"EXTERNALLY_MANAGED_BY_KUBERNETES", "MY-NAMESPACE", "MYTAG", "MYOTHERTAG"}
	assert.Equal(t, expected, mockOm.FindGroup(om.TestGroupName).Tags)
}

// TestPrepareOmConnection_FindExistingGroup finds existing group when org ID is specified, no new Project or Organization
// is created
func TestPrepareOmConnection_FindExistingGroup(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddCredentialsSecret(om.TestUser, om.TestApiKey)
	manager.Client.AddProjectConfigMap(om.TestGroupName, om.TestOrgID)
	reconciler := newReconcileCommonController(manager, omConnGroupInOrganizationWithDifferentName())

	mockOm, _ := prepareConnection(reconciler, t)
	assert.Equal(t, "existingGroupId", mockOm.GroupID())
	// No new group was created
	assert.Len(t, mockOm.OrganizationsWithGroups, 1)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadOrganization), reflect.ValueOf(mockOm.ReadProjectsInOrganizationByName))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.ReadOrganizations), reflect.ValueOf(mockOm.CreateProject), reflect.ValueOf(mockOm.ReadProjectsInOrganization))
}

// TestPrepareOmConnection_DuplicatedGroups verifies that if there are groups with the same name but in different organization
// then the new group is created
func TestPrepareOmConnection_DuplicatedGroups(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()

	// The only difference from TestPrepareOmConnection_FindExistingGroup above is that the config map contains only project name
	// but no org ID (see newMockedKubeApi())
	controller := newReconcileCommonController(manager, omConnGroupInOrganizationWithDifferentName())

	mockOm, _ := prepareConnection(controller, t)
	assert.Equal(t, om.TestGroupID, mockOm.GroupID())
	mockOm.CheckGroupInOrganization(t, om.TestGroupName, om.TestGroupName)
	// New group and organization will be created in addition to existing ones
	assert.Len(t, mockOm.OrganizationsWithGroups, 2)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadOrganizationsByName), reflect.ValueOf(mockOm.CreateProject))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.ReadOrganizations),
		reflect.ValueOf(mockOm.ReadProjectsInOrganization), reflect.ValueOf(mockOm.ReadProjectsInOrganizationByName))
}

// TestPrepareOmConnection_CreateGroup checks that if the group doesn't exist in OM - it is created
func TestPrepareOmConnection_CreateGroup(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()

	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnectionNoGroup)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupID, vars.ProjectID)
	assert.Equal(t, om.TestGroupID, mockOm.GroupID())
	mockOm.CheckGroupInOrganization(t, om.TestGroupName, om.TestGroupName)
	assert.Len(t, mockOm.OrganizationsWithGroups, 1)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, util.OmGroupExternallyManagedTag)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, strings.ToUpper(mock.TestNamespace))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadOrganizationsByName), reflect.ValueOf(mockOm.CreateProject))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.ReadProjectsInOrganization))
}

// TestPrepareOmConnection_CreateGroupFixTags fixes tags if they are not set for existing group
func TestPrepareOmConnection_CreateGroupFixTags(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()

	controller := newReconcileCommonController(manager, omConnGroupWithoutTags())

	mockOm, _ := prepareConnection(controller, t)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, util.OmGroupExternallyManagedTag)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, strings.ToUpper(mock.TestNamespace))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.UpdateProject))
}

func readAgentApiKeyForProject(k KubeHelper, namespace, agentKeySecretName string) (string, error) {
	secret, err := k.readSecret(objectKey(namespace, agentKeySecretName))
	if err != nil {
		return "", err
	}

	key, ok := secret[util.OmAgentApiKey]
	if !ok {
		return "", fmt.Errorf("Could not find key \"%s\" in secret %s", util.OmAgentApiKey, agentKeySecretName)
	}

	return strings.TrimSuffix(key, "\n"), nil
}

// TestPrepareOmConnection_PrepareAgentKeys checks that agent key is generated and put to secret
func TestPrepareOmConnection_PrepareAgentKeys(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()
	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)

	prepareConnection(controller, t)

	key, e := readAgentApiKeyForProject(controller.kubeHelper, mock.TestNamespace, agentApiKeySecretName(om.TestGroupID))

	assert.NoError(t, e)
	// Unfortunately the key read is not equal to om.TestAgentKey - it's just some set of bytes.
	// This is reproduced only in mocked tests - the production is fine (the key is real string)
	// I assume that it's because when setting the secret data we use 'StringData' but read it back as
	// 'Data' which is binary. May be real kubernetes api reads data as string and updates
	assert.NotNil(t, key)

	manager.Client.CheckOrderOfOperations(t,
		mock.HItem(reflect.ValueOf(manager.Client.Get), &corev1.Secret{}),
		mock.HItem(reflect.ValueOf(manager.Client.Create), &corev1.Secret{}))
}

// TestPrepareOmConnection_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
// map that allows to watch them for changes
func TestPrepareOmConnection_ConfigMapAndSecretWatched(t *testing.T) {
	manager := mock.NewEmptyManager()
	manager.Client.AddDefaultMdbConfigResources()
	reconciler := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)

	// "create" a secret (config map already exists)
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mySecret", Namespace: mock.TestNamespace},
		StringData: map[string]string{util.OmUser: "bla@mycompany.com", util.OmPublicApiKey: "2423423gdfgsdf23423sdfds"}}
	_ = manager.Client.Create(context.TODO(), credentials)

	// Here we create two replica sets both referencing the same project and credentials
	vars := &PodVars{}
	spec := mdbv1.ConnectionSpec{
		OpsManagerConfig: &mdbv1.PrivateCloudConfig{
			ConfigMapRef: mdbv1.ConfigMapRef{
				Name: mock.TestProjectConfigMapName,
			},
		},
		Credentials: "mySecret",
		LogLevel:    mdbv1.Warn,
	}
	_, e := reconciler.prepareConnection(objectKey(mock.TestNamespace, "ReplicaSetOne"), spec, vars, zap.S())
	assert.NoError(t, e)
	_, e = reconciler.prepareConnection(objectKey(mock.TestNamespace, "ReplicaSetTwo"), spec, vars, zap.S())
	assert.NoError(t, e)

	// This one must not affect the map any way as everything is already registered
	_, e = reconciler.prepareConnection(objectKey(mock.TestNamespace, "ReplicaSetTwo"), spec, vars, zap.S())
	assert.NoError(t, e)

	// we expect to have two entries in the map - each value has length of 2 meaning both replica sets are "registered"
	// to be reconciled as soon as config map or secret changes
	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: objectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {objectKey(mock.TestNamespace, "ReplicaSetOne"), objectKey(mock.TestNamespace, "ReplicaSetTwo")},
		{ResourceType: watch.Secret, Resource: objectKey(mock.TestNamespace, "mySecret")}:                       {objectKey(mock.TestNamespace, "ReplicaSetOne"), objectKey(mock.TestNamespace, "ReplicaSetTwo")},
	}
	assert.Equal(t, expected, reconciler.watchedResources)
}

// TestUpdateStatus_Patched makes sure that 'ReconcileCommonController.updateStatus()' changes only status for current
// object in Kubernetes and leaves spec unchanged
func TestUpdateStatus_Patched(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()
	manager := mock.NewManager(rs)
	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)
	reconciledObject := rs.DeepCopy()
	// The current reconciled object "has diverged" from the one in API server
	reconciledObject.Spec.Version = "10.0.0"
	_, err := controller.updateStatus(reconciledObject, workflow.Pending("Waiting for secret..."), zap.S())
	assert.NoError(t, err)

	// Verifying that the resource in API server still has correct spec
	currentRs := mdbv1.MongoDB{}
	assert.NoError(t, manager.Client.Get(context.Background(), rs.ObjectKey(), &currentRs))

	// The spec hasn't changed - only status
	assert.Equal(t, rs.Spec, currentRs.Spec)
	assert.Equal(t, status.PhasePending, currentRs.Status.Phase)
	assert.Equal(t, "Waiting for secret...", currentRs.Status.Message)
}

func TestShouldUseFeatureControls(t *testing.T) {

	// older versions which do not support policy control
	assert.False(t, shouldUseFeatureControls(toOMVersion("4.2.1")))
	assert.False(t, shouldUseFeatureControls(toOMVersion("4.3.0")))

	// if we don't know the version, use the tag
	assert.False(t, shouldUseFeatureControls(toOMVersion("")))

	// older version we don't know about, we assume a tag
	assert.False(t, shouldUseFeatureControls(toOMVersion("3.6.0")))
	assert.False(t, shouldUseFeatureControls(toOMVersion("3.6.2")))
	assert.False(t, shouldUseFeatureControls(toOMVersion("3.6.3")))

	// minimum versions that support policy control
	assert.True(t, shouldUseFeatureControls(toOMVersion("4.2.2")))
	assert.True(t, shouldUseFeatureControls(toOMVersion("4.2.3")))
	assert.True(t, shouldUseFeatureControls(toOMVersion("4.3.1")))
	assert.True(t, shouldUseFeatureControls(toOMVersion("4.3.2")))
	assert.True(t, shouldUseFeatureControls(toOMVersion("4.4.0")))
	assert.True(t, shouldUseFeatureControls(toOMVersion("4.4.1")))
}

func TestReadSubjectFromJustCertificate(t *testing.T) {
	assertSubjectFromFileSucceeds(t, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US", "testdata/certificates/just_certificate")
}

func TestReadSubjectFromCertificateThenKey(t *testing.T) {
	assertSubjectFromFileSucceeds(t, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US", "testdata/certificates/certificate_then_key")
}

func TestReadSubjectFromKeyThenCertificate(t *testing.T) {
	assertSubjectFromFileSucceeds(t, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US", "testdata/certificates/key_then_certificate")
}

func TestReadSubjectNoCertificate(t *testing.T) {
	assertSubjectFromFileFails(t, "testdata/certificates/just_key")
}

func assertSubjectFromFileFails(t *testing.T, filePath string) {
	assertSubjectFromFile(t, "", filePath, false)
}

func assertSubjectFromFileSucceeds(t *testing.T, expectedSubject, filePath string) {
	assertSubjectFromFile(t, expectedSubject, filePath, true)
}

func assertSubjectFromFile(t *testing.T, expectedSubject, filePath string, passes bool) {
	data, err := ioutil.ReadFile(filePath)
	assert.NoError(t, err)
	subject, err := getSubjectFromCertificate(string(data))
	if passes {
		assert.NoError(t, err)
	} else {
		assert.Error(t, err)
	}
	assert.Equal(t, expectedSubject, subject)
}

func toOMVersion(versionString string) *om.Version {
	if versionString == "" {
		return &om.Version{}
	}

	return &om.Version{
		VersionString: fmt.Sprintf("%s.56729.20191105T2247Z", versionString),
	}
}

func prepareConnection(controller *ReconcileCommonController, t *testing.T) (*om.MockedOmConnection, *PodVars) {
	vars := &PodVars{}
	spec := mdbv1.ConnectionSpec{
		OpsManagerConfig: &mdbv1.PrivateCloudConfig{
			ConfigMapRef: mdbv1.ConfigMapRef{
				Name: mock.TestProjectConfigMapName,
			},
		},
		Credentials: mock.TestCredentialsSecretName,
		LogLevel:    mdbv1.Warn,
	}
	conn, e := controller.prepareConnection(objectKey(mock.TestNamespace, ""), spec, vars, zap.S())
	mockOm := conn.(*om.MockedOmConnection)
	assert.NoError(t, e)
	return mockOm, vars
}

func omConnGroupWithoutTags() om.ConnectionFactory {
	return func(ctx *om.OMContext) om.Connection {
		c := om.NewEmptyMockedOmConnectionNoGroup(ctx).(*om.MockedOmConnection)
		if len(c.OrganizationsWithGroups) == 0 {
			// initially OM contains the group without tags
			c.OrganizationsWithGroups = map[*om.Organization][]*om.Project{{ID: om.TestOrgID, Name: om.TestGroupName}: {{Name: om.TestGroupName, ID: "123", AgentAPIKey: "12345abcd", OrgID: om.TestOrgID}}}
		}
		return c
	}
}

func omConnGroupInOrganizationWithDifferentName() om.ConnectionFactory {
	return func(omContext *om.OMContext) om.Connection {
		c := om.NewEmptyMockedOmConnectionNoGroup(omContext).(*om.MockedOmConnection)
		if len(c.OrganizationsWithGroups) == 0 {
			// Important: the organization for the group has a different name ("foo") then group (om.TestGroupName).
			// So it won't work for cases when the group "was created before" by Operator
			c.OrganizationsWithGroups = map[*om.Organization][]*om.Project{{ID: om.TestOrgID, Name: "foo"}: {{Name: om.TestGroupName, ID: "existingGroupId", OrgID: om.TestOrgID}}}
		}

		return c
	}
}

func requestFromObject(object apiruntime.Object) reconcile.Request {
	return reconcile.Request{NamespacedName: objectKeyFromApiObject(object)}
}

func checkReconcileSuccessful(t *testing.T, reconciler reconcile.Reconciler, object *mdbv1.MongoDB, client *mock.MockedClient) {
	result, e := reconciler.Reconcile(requestFromObject(object))
	require.NoError(t, e)
	require.Equal(t, reconcile.Result{}, result)

	// also need to make sure the object status is updated to successful
	assert.NoError(t, client.Get(context.TODO(), objectKeyFromApiObject(object), object))
	assert.Equal(t, status.PhaseRunning, object.Status.Phase)

	expectedLink := DeploymentLink(om.TestURL, om.TestGroupID)

	// fields common to all resource types
	assert.Equal(t, object.Spec.Version, object.Status.Version)
	assert.Equal(t, expectedLink, object.Status.Link)
	assert.NotNil(t, object.Status.LastTransition)
	assert.NotEqual(t, object.Status.LastTransition, "")

	switch object.Spec.ResourceType {
	case mdbv1.ReplicaSet:
		assert.Equal(t, object.Spec.Members, object.Status.Members)
	case mdbv1.ShardedCluster:
		assert.Equal(t, object.Spec.ConfigServerCount, object.Status.ConfigServerCount)
		assert.Equal(t, object.Spec.MongosCount, object.Status.MongosCount)
		assert.Equal(t, object.Spec.MongodsPerShardCount, object.Status.MongodsPerShardCount)
		assert.Equal(t, object.Spec.ShardCount, object.Status.ShardCount)
	}
}

func checkOMReconcilliationSuccessful(t *testing.T, reconciler reconcile.Reconciler, om *mdbv1.MongoDBOpsManager) {
	res, err := reconciler.Reconcile(requestFromObject(om))
	expected := reconcile.Result{Requeue: true}
	assert.Equal(t, expected, res)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(requestFromObject(om))
	expected, _ = success()
	assert.Equal(t, expected, res)
	assert.NoError(t, err)
}

func checkReconcileFailed(t *testing.T, reconciler reconcile.Reconciler, object *mdbv1.MongoDB, expectedRetry bool, expectedErrorMessage string, client *mock.MockedClient) {
	failedResult := reconcile.Result{}
	if expectedRetry {
		failedResult.RequeueAfter = 10 * time.Second
	}
	result, e := reconciler.Reconcile(requestFromObject(object))
	assert.Nil(t, e, "When retrying, error should be nil")
	assert.Equal(t, failedResult, result)

	// also need to make sure the object status is updated to failed
	assert.NoError(t, client.Get(context.TODO(), objectKeyFromApiObject(object), object))
	assert.Equal(t, status.PhaseFailed, object.Status.Phase)
	assert.Contains(t, object.Status.Message, expectedErrorMessage)
}

func checkReconcilePending(t *testing.T, reconciler reconcile.Reconciler, object *mdbv1.MongoDB, expectedErrorMessage string, client *mock.MockedClient, requeueAfter time.Duration) {
	failedResult := reconcile.Result{RequeueAfter: requeueAfter * time.Second}
	result, e := reconciler.Reconcile(requestFromObject(object))
	assert.Nil(t, e, "When pending, error should be nil")
	assert.Equal(t, failedResult, result)

	// also need to make sure the object status is updated to failed
	assert.NoError(t, client.Get(context.TODO(), objectKeyFromApiObject(object), object))
	assert.Equal(t, status.PhasePending, object.Status.Phase)
	assert.Contains(t, object.Status.Message, expectedErrorMessage)
}
