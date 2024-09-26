package operator

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/10gen/ops-manager-kubernetes/pkg/agentVersionManagement"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"golang.org/x/xerrors"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/deployment"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const OperatorNamespace = "operatorNs"

func init() {
	util.OperatorVersion = "9.9.9-test"
	_ = os.Setenv(util.CurrentNamespace, OperatorNamespace)
}

func TestEnsureTagAdded(t *testing.T) {
	ctx := context.Background()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := newReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	// normal tag
	err := connection.EnsureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "myTag", zap.S())
	assert.NoError(t, err)

	// long tag
	err = connection.EnsureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "LOOKATTHISTRINGTHATISTOOLONGFORTHEFIELD", zap.S())
	assert.NoError(t, err)

	expected := []string{"EXTERNALLY_MANAGED_BY_KUBERNETES", "MY-NAMESPACE", "MYTAG", "LOOKATTHISTRINGTHATISTOOLONGFORT"}
	assert.Equal(t, expected, mockOm.FindGroup(om.TestGroupName).Tags)
}

func TestEnsureTagAddedDuplicates(t *testing.T) {
	ctx := context.Background()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	opsManagerController := newReconcileCommonController(ctx, kubeClient)

	mockOm, _ := prepareConnection(ctx, opsManagerController, omConnectionFactory.GetConnectionFunc, t)
	err := connection.EnsureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "MYTAG", zap.S())
	assert.NoError(t, err)
	err = connection.EnsureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "MYTAG", zap.S())
	assert.NoError(t, err)
	err = connection.EnsureTagAdded(mockOm, mockOm.FindGroup(om.TestGroupName), "MYOTHERTAG", zap.S())
	assert.NoError(t, err)
	expected := []string{"EXTERNALLY_MANAGED_BY_KUBERNETES", "MY-NAMESPACE", "MYTAG", "MYOTHERTAG"}
	assert.Equal(t, expected, mockOm.FindGroup(om.TestGroupName).Tags)
}

// TestPrepareOmConnection_FindExistingGroup finds existing group when org ID is specified, no new Project or Organization
// is created
func TestPrepareOmConnection_FindExistingGroup(t *testing.T) {
	ctx := context.Background()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, []client.Object{
		mock.GetProjectConfigMap(mock.TestProjectConfigMapName, om.TestGroupName, om.TestOrgID),
		mock.GetCredentialsSecret(om.TestUser, om.TestApiKey),
	}...)
	omConnectionFactory.SetPostCreateHook(func(c om.Connection) {
		// Important: the organization for the group has a different name ("foo") then group (om.TestGroupName).
		// So it won't work for cases when the group "was created before" by Operator
		c.(*om.MockedOmConnection).OrganizationsWithGroups = map[*om.Organization][]*om.Project{{ID: om.TestOrgID, Name: "foo"}: {{Name: om.TestGroupName, ID: "existing-group-id", OrgID: om.TestOrgID}}}
	})

	controller := newReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)
	assert.Equal(t, "existing-group-id", mockOm.GroupID())
	// No new group was created
	assert.Len(t, mockOm.OrganizationsWithGroups, 1)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadOrganization), reflect.ValueOf(mockOm.ReadProjectsInOrganizationByName))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.ReadOrganizations), reflect.ValueOf(mockOm.CreateProject), reflect.ValueOf(mockOm.ReadProjectsInOrganization))
}

// TestPrepareOmConnection_DuplicatedGroups verifies that if there are groups with the same name but in different organization
// then the new group is created
func TestPrepareOmConnection_DuplicatedGroups(t *testing.T) {
	ctx := context.Background()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientWithInterceptor(omConnectionFactory, []client.Object{
		mock.GetProjectConfigMap(mock.TestProjectConfigMapName, om.TestGroupName, ""),
		mock.GetCredentialsSecret(om.TestUser, om.TestApiKey),
	}...)
	omConnectionFactory.SetPostCreateHook(func(c om.Connection) {
		// Important: the organization for the group has a different name ("foo") then group (om.TestGroupName).
		// So it won't work for cases when the group "was created before" by Operator
		c.(*om.MockedOmConnection).OrganizationsWithGroups = map[*om.Organization][]*om.Project{{ID: om.TestOrgID, Name: "foo"}: {{Name: om.TestGroupName, ID: "existing-group-id", OrgID: om.TestOrgID}}}
	})

	// The only difference from TestPrepareOmConnection_FindExistingGroup above is that the config map contains only project name
	// but no org ID (see newMockedKubeApi())
	controller := newReconcileCommonController(ctx, kubeClient)

	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)
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
	ctx := context.Background()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	omConnectionFactory.SetPostCreateHook(func(c om.Connection) {
		c.(*om.MockedOmConnection).OrganizationsWithGroups = map[*om.Organization][]*om.Project{}
	})

	controller := newReconcileCommonController(ctx, kubeClient)

	mockOm, vars := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	assert.Equal(t, om.TestGroupID, vars.ProjectID)
	assert.Equal(t, om.TestGroupID, mockOm.GroupID())
	mockOm.CheckGroupInOrganization(t, om.TestGroupName, om.TestGroupName)
	assert.Len(t, mockOm.OrganizationsWithGroups, 1)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, strings.ToUpper(mock.TestNamespace))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadOrganizationsByName), reflect.ValueOf(mockOm.CreateProject))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.ReadProjectsInOrganization))
}

// TestPrepareOmConnection_CreateGroupFixTags fixes tags if they are not set for existing group
func TestPrepareOmConnection_CreateGroupFixTags(t *testing.T) {
	ctx := context.Background()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	omConnectionFactory.SetPostCreateHook(func(c om.Connection) {
		c.(*om.MockedOmConnection).OrganizationsWithGroups = map[*om.Organization][]*om.Project{
			{
				ID:   om.TestOrgID,
				Name: om.TestGroupName,
			}: {
				{
					Name:        om.TestGroupName,
					ID:          "123",
					AgentAPIKey: "12345abcd",
					OrgID:       om.TestOrgID,
				},
			},
		}
	})

	controller := newReconcileCommonController(ctx, kubeClient)

	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, strings.ToUpper(mock.TestNamespace))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.UpdateProject))
}

func readAgentApiKeyForProject(ctx context.Context, client kubernetesClient.Client, namespace, agentKeySecretName string) (string, error) {
	secret, err := client.GetSecret(ctx, kube.ObjectKey(namespace, agentKeySecretName))
	if err != nil {
		return "", err
	}

	key, ok := secret.Data[util.OmAgentApiKey]
	if !ok {
		return "", xerrors.Errorf("Could not find key \"%s\" in secret %s", util.OmAgentApiKey, agentKeySecretName)
	}

	return strings.TrimSuffix(string(key), "\n"), nil
}

// TestPrepareOmConnection_PrepareAgentKeys checks that agent key is generated and put to secret
func TestPrepareOmConnection_PrepareAgentKeys(t *testing.T) {
	ctx := context.Background()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := newReconcileCommonController(ctx, kubeClient)

	prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)
	key, e := readAgentApiKeyForProject(ctx, controller.client, mock.TestNamespace, agents.ApiKeySecretName(om.TestGroupID))

	assert.NoError(t, e)
	// Unfortunately the key read is not equal to om.TestAgentKey - it's just some set of bytes.
	// This is reproduced only in mocked tests - the production is fine (the key is real string)
	// I assume that it's because when setting the secret data we use 'StringData' but read it back as
	// 'Data' which is binary. May be real kubernetes api reads data as string and updates
	assert.NotNil(t, key)
}

// TestUpdateStatus_Patched makes sure that 'ReconcileCommonController.updateStatus()' changes only status for current
// object in Kubernetes and leaves spec unchanged
func TestUpdateStatus_Patched(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()
	kubeClient, _ := mock.NewDefaultFakeClient(rs)
	controller := newReconcileCommonController(ctx, kubeClient)
	reconciledObject := rs.DeepCopy()
	// The current reconciled object "has diverged" from the one in API server
	reconciledObject.Spec.Version = "10.0.0"
	_, err := controller.updateStatus(ctx, reconciledObject, workflow.Pending("Waiting for secret..."), zap.S())
	assert.NoError(t, err)

	// Verifying that the resource in API server still has the correct spec
	currentRs := mdbv1.MongoDB{}
	assert.NoError(t, kubeClient.Get(ctx, rs.ObjectKey(), &currentRs))

	// The spec hasn't changed - only status
	assert.Equal(t, rs.Spec, currentRs.Spec)
	assert.Equal(t, status.PhasePending, currentRs.Status.Phase)
	assert.Equal(t, "Waiting for secret...", currentRs.Status.Message)
}

func TestReadSubjectFromJustCertificate(t *testing.T) {
	ctx := context.Background()
	assertSubjectFromFileSucceeds(ctx, t, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US", "testdata/certificates/just_certificate")
}

func TestReadSubjectFromCertificateThenKey(t *testing.T) {
	ctx := context.Background()
	assertSubjectFromFileSucceeds(ctx, t, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US", "testdata/certificates/certificate_then_key")
}

func TestReadSubjectFromKeyThenCertificate(t *testing.T) {
	ctx := context.Background()
	assertSubjectFromFileSucceeds(ctx, t, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US", "testdata/certificates/key_then_certificate")
}

func TestReadSubjectFromCertInStrictlyRFC2253(t *testing.T) {
	ctx := context.Background()
	assertSubjectFromFileSucceeds(ctx, t, "CN=mms-agent-cert,O=MongoDB-agent,OU=TSE,L=New Delhi,ST=New Delhi,C=IN", "testdata/certificates/cert_rfc2253")
}

func TestReadSubjectNoCertificate(t *testing.T) {
	assertSubjectFromFileFails(t, "testdata/certificates/just_key")
}

func TestDontSendNilPrivileges(t *testing.T) {
	ctx := context.Background()
	customRole := mdbv1.MongoDbRole{
		Role:                       "foo",
		AuthenticationRestrictions: []mdbv1.AuthenticationRestriction{},
		Db:                         "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "readWriteAnyDatabase",
		}},
	}
	assert.Nil(t, customRole.Privileges)
	rs := DefaultReplicaSetBuilder().SetRoles([]mdbv1.MongoDbRole{customRole}).Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := newReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)
	ensureRoles(rs.Spec.Security.Roles, mockOm, &zap.SugaredLogger{})
	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDbRole)
	assert.True(t, ok)
	assert.NotNil(t, roles[0].Privileges)
}

func TestSecretWatcherWithAllResources(t *testing.T) {
	ctx := context.Background()
	caName := "custom-ca"
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().SetTLSCA(caName).Build()
	rs.Spec.Security.Authentication.InternalCluster = "X509"
	kubeClient, _ := mock.NewDefaultFakeClient(rs)
	controller := newReconcileCommonController(ctx, kubeClient)

	controller.SetupCommonWatchers(rs, nil, nil, rs.Name)

	// TODO: unify the watcher setup with the secret creation/mounting code in database creation
	memberCert := rs.GetSecurity().MemberCertificateSecretName(rs.Name)
	internalAuthCert := rs.GetSecurity().InternalClusterAuthSecretName(rs.Name)
	agentCert := rs.GetSecurity().AgentClientCertificateSecretName(rs.Name).Name

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, caName)}:                        {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, memberCert)}:                       {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, internalAuthCert)}:                 {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, agentCert)}:                        {kube.ObjectKey(mock.TestNamespace, rs.Name)},
	}

	assert.Equal(t, expected, controller.resourceWatcher.GetWatchedResources())
}

func TestSecretWatcherWithSelfProvidedTLSSecretNames(t *testing.T) {
	ctx := context.Background()
	caName := "custom-ca"

	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().SetTLSCA(caName).Build()
	kubeClient, _ := mock.NewDefaultFakeClient(rs)
	controller := newReconcileCommonController(ctx, kubeClient)

	controller.SetupCommonWatchers(rs, func() []string {
		return []string{"a-secret"}
	}, nil, rs.Name)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, caName)}:                        {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, "a-secret")}:                       {kube.ObjectKey(mock.TestNamespace, rs.Name)},
	}

	assert.Equal(t, expected, controller.resourceWatcher.GetWatchedResources())
}

func assertSubjectFromFileFails(t *testing.T, filePath string) {
	assertSubjectFromFile(t, "", filePath, false)
}

func assertSubjectFromFileSucceeds(ctx context.Context, t *testing.T, expectedSubject, filePath string) {
	assertSubjectFromFile(t, expectedSubject, filePath, true)
}

func assertSubjectFromFile(t *testing.T, expectedSubject, filePath string, passes bool) {
	data, err := os.ReadFile(filePath)
	assert.NoError(t, err)
	subject, err := getSubjectFromCertificate(string(data))
	if passes {
		assert.NoError(t, err)
	} else {
		assert.Error(t, err)
	}
	assert.Equal(t, expectedSubject, subject)
}

func prepareConnection(ctx context.Context, controller *ReconcileCommonController, omConnectionFunc om.ConnectionFactory, t *testing.T) (*om.MockedOmConnection, *env.PodEnvVars) {
	projectConfig, err := project.ReadProjectConfig(ctx, controller.client, kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName), "mdb-name")
	assert.NoError(t, err)
	credsConfig, err := project.ReadCredentials(ctx, controller.SecretClient, kube.ObjectKey(mock.TestNamespace, mock.TestCredentialsSecretName), &zap.SugaredLogger{})
	assert.NoError(t, err)

	conn, _, e := connection.PrepareOpsManagerConnection(ctx, controller.SecretClient, projectConfig, credsConfig, omConnectionFunc, mock.TestNamespace, zap.S())
	mockOm := conn.(*om.MockedOmConnection)
	assert.NoError(t, e)
	return mockOm, newPodVars(conn, projectConfig, mdbv1.Warn)
}

func requestFromObject(object metav1.Object) reconcile.Request {
	return reconcile.Request{NamespacedName: mock.ObjectKeyFromApiObject(object)}
}

func testConnectionSpec() mdbv1.ConnectionSpec {
	return mdbv1.ConnectionSpec{
		SharedConnectionSpec: mdbv1.SharedConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{
					Name: mock.TestProjectConfigMapName,
				},
			},
		},
		Credentials: mock.TestCredentialsSecretName,
	}
}

func checkReconcileSuccessful(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, object *mdbv1.MongoDB, client client.Client) {
	err := client.Update(ctx, object)
	require.NoError(t, err)

	result, err := reconciler.Reconcile(ctx, requestFromObject(object))
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)

	// also need to make sure the object status is updated to successful
	assert.NoError(t, client.Get(ctx, mock.ObjectKeyFromApiObject(object), object))
	assert.Equal(t, status.PhaseRunning, object.Status.Phase)

	expectedLink := deployment.Link(om.TestURL, om.TestGroupID)

	// fields common to all resource types
	assert.Equal(t, object.Spec.Version, object.Status.Version)
	assert.Equal(t, expectedLink, object.Status.Link)
	assert.NotNil(t, object.Status.LastTransition)
	assert.NotEqual(t, object.Status.LastTransition, "")

	assert.Equal(t, object.GetGeneration(), object.Status.ObservedGeneration)

	switch object.Spec.ResourceType {
	case mdbv1.ReplicaSet:
		assert.Equal(t, object.Spec.Members, object.Status.Members)
	case mdbv1.ShardedCluster:
		if object.Spec.IsMultiCluster() {
			assert.Equal(t, object.Spec.ShardCount, object.Status.ShardCount)
		} else {
			assert.Equal(t, object.Spec.ConfigServerCount, object.Status.ConfigServerCount)
			assert.Equal(t, object.Spec.MongosCount, object.Status.MongosCount)
			assert.Equal(t, object.Spec.MongodsPerShardCount, object.Status.MongodsPerShardCount)
			assert.Equal(t, object.Spec.ShardCount, object.Status.ShardCount)
		}
	}
	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(object), object))
}

func checkOMReconciliationSuccessful(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, om *omv1.MongoDBOpsManager, client client.Client) {
	res, err := reconciler.Reconcile(ctx, requestFromObject(om))
	expected := reconcile.Result{Requeue: true}
	assert.Equal(t, expected, res)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(ctx, requestFromObject(om))
	expected = reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}
	assert.Equal(t, expected, res)
	assert.NoError(t, err)

	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(om), om))
}

func checkOMReconciliationInvalid(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, om *omv1.MongoDBOpsManager, client client.Client) {
	res, err := reconciler.Reconcile(ctx, requestFromObject(om))
	expected, _ := workflow.OK().Requeue().ReconcileResult()
	assert.Equal(t, expected, res)
	assert.NoError(t, err)

	res, err = reconciler.Reconcile(ctx, requestFromObject(om))
	expected, _ = workflow.Invalid("doesn't matter").ReconcileResult()
	assert.Equal(t, expected, res)
	assert.NoError(t, err)

	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(om), om))
}

func checkOMReconciliationPending(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, om *omv1.MongoDBOpsManager) {
	res, err := reconciler.Reconcile(ctx, requestFromObject(om))
	assert.NoError(t, err)
	assert.True(t, res.Requeue || res.RequeueAfter == time.Duration(10000000000))
}

func checkReconcileFailed(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, object *mdbv1.MongoDB, expectedRetry bool, expectedErrorMessage string, client client.Client) {
	failedResult := reconcile.Result{}
	if expectedRetry {
		failedResult.RequeueAfter = 10 * time.Second
	}
	result, e := reconciler.Reconcile(ctx, requestFromObject(object))
	assert.Nil(t, e, "When retrying, error should be nil")
	assert.Equal(t, failedResult, result)

	// also need to make sure the object status is updated to failed
	assert.NoError(t, client.Get(ctx, mock.ObjectKeyFromApiObject(object), object))
	assert.Equal(t, status.PhaseFailed, object.Status.Phase)
	assert.Contains(t, object.Status.Message, expectedErrorMessage)
}

func checkReconcilePending(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, object *mdbv1.MongoDB, expectedErrorMessage string, client client.Client, requeueAfter time.Duration) {
	failedResult := reconcile.Result{RequeueAfter: requeueAfter * time.Second}
	result, e := reconciler.Reconcile(ctx, requestFromObject(object))
	assert.Nil(t, e, "When pending, error should be nil")
	assert.Equal(t, failedResult, result)

	// also need to make sure the object status is updated to failed
	assert.NoError(t, client.Get(ctx, mock.ObjectKeyFromApiObject(object), object))
	assert.Equal(t, status.PhasePending, object.Status.Phase)
	assert.Contains(t, object.Status.Message, expectedErrorMessage)
}

func getWatch(namespace string, resourceName string, t watch.Type) watch.Object {
	configSecret := watch.Object{
		ResourceType: t,
		Resource: types.NamespacedName{
			Namespace: namespace,
			Name:      resourceName,
		},
	}
	return configSecret
}

type testReconciliationResources struct {
	Resource          *mdbv1.MongoDB
	ReconcilerFactory func(rs *mdbv1.MongoDB) (reconcile.Reconciler, kubernetesClient.Client)
}

// agentVersionMappingTest is a helper function to verify that the version mapping mechanism works correctly in controllers
// in case retrieving the version fails, the user should have the possibility to override the image in the pod specs
func agentVersionMappingTest(ctx context.Context, t *testing.T, defaultResource testReconciliationResources, overridenResource testReconciliationResources) {
	nonExistingPath := "/foo/bar/foo"

	t.Run("Static architecture, version retrieving fails, image is overriden, reconciliation should succeed", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
		t.Setenv(agentVersionManagement.MappingFilePathEnv, nonExistingPath)
		overridenReconciler, overridenClient := overridenResource.ReconcilerFactory(overridenResource.Resource)
		checkReconcileSuccessful(ctx, t, overridenReconciler, overridenResource.Resource, overridenClient)
	})

	t.Run("Static architecture, version retrieving fails, image is not overriden, reconciliation should fail", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
		t.Setenv(agentVersionManagement.MappingFilePathEnv, nonExistingPath)
		defaultReconciler, defaultClient := defaultResource.ReconcilerFactory(defaultResource.Resource)
		checkReconcileFailed(ctx, t, defaultReconciler, defaultResource.Resource, true, "", defaultClient)
	})

	t.Run("Static architecture, version retrieving succeeds, image is not overriden, reconciliation should succeed", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
		defaultReconciler, defaultClient := defaultResource.ReconcilerFactory(defaultResource.Resource)
		checkReconcileSuccessful(ctx, t, defaultReconciler, defaultResource.Resource, defaultClient)
	})

	t.Run("Non-Static architecture, version retrieving fails, image is not overriden, reconciliation should succeed", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))
		t.Setenv(agentVersionManagement.MappingFilePathEnv, nonExistingPath)
		defaultReconciler, defaultClient := defaultResource.ReconcilerFactory(defaultResource.Resource)
		checkReconcileSuccessful(ctx, t, defaultReconciler, defaultResource.Resource, defaultClient)
	})
}

func testConcurrentReconciles(ctx context.Context, t *testing.T, client client.Client, reconciler reconcile.Reconciler, objects ...client.Object) {
	for _, object := range objects {
		err := mock.CreateOrUpdate(ctx, client, object)
		require.NoError(t, err)
	}

	// Let's have one reconcile first, such that we have the same object reconciles multiple times
	_, err := reconciler.Reconcile(ctx, requestFromObject(objects[0]))
	require.NoError(t, err)
	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(objects[0]), objects[0]))

	var wg sync.WaitGroup

	// we need to increase the chance of races to happen.
	// therefore, we reconcile a lot of times a lot of resources concurrently
	// Note: we don't concurrently reconcile the same resource
	for i := 0; i < 5; i++ {
		wg.Add(len(objects))
		for _, object := range objects {
			object := object
			go func() {
				_, err := reconciler.Reconcile(ctx, requestFromObject(object))
				assert.NoError(t, err)
				wg.Done()
			}()
		}
		wg.Wait()
	}
}
