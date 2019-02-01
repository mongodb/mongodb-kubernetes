package operator

import (
	"context"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/pkg/errors"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	v12 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/require"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
)

func init() {
	util.OperatorVersion = "testVersion"
}

// TestPrepareOmConnection_FindExistingGroup finds existing group when org ID is specified
func TestPrepareOmConnection_FindExistingGroup(t *testing.T) {
	mockedOmConnection := omConnGroupInOrganizationWithDifferentName()

	reconciler := newReconcileCommonController(newMockedManagerDetailed(nil, om.TestGroupName, om.TestOrgID), mockedOmConnection)

	mockOm, _ := prepareConnection(reconciler, t)
	assert.Equal(t, "existingGroupId", mockOm.GroupID())
	// No new group was created
	assert.Len(t, mockOm.AllGroups, 1)
	assert.Len(t, mockOm.AllOrganizations, 1)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_DuplicatedGroups verifies that if there are groups with the same name but in different organization
// then the new group is created
func TestPrepareOmConnection_DuplicatedGroups(t *testing.T) {
	mockedOmConnection := omConnGroupInOrganizationWithDifferentName()

	// The only difference from TestPrepareOmConnection_FindExistingGroup^ is that the config map contains only project name
	// but no org ID (see newMockedKubeApi())
	controller := newReconcileCommonController(newMockedManager(nil), mockedOmConnection)

	mockOm, _ := prepareConnection(controller, t)
	assert.Equal(t, om.TestGroupID, mockOm.GroupID())
	assert.NotNil(t, mockOm.FindGroup(om.TestGroupName))
	// New group and organization will be created in addition to existing ones
	assert.Len(t, mockOm.AllGroups, 2)
	assert.Len(t, mockOm.AllOrganizations, 2)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.ReadOrganizations), reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_CreateGroup checks that if the group doesn't exist in OM - it is created
func TestPrepareOmConnection_CreateGroup(t *testing.T) {
	mockedOmConnection := om.NewEmptyMockedOmConnectionNoGroup

	controller := newReconcileCommonController(newMockedManager(nil), mockedOmConnection)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupID, vars.ProjectID)
	assert.Equal(t, om.TestGroupID, mockOm.GroupID())
	require.NotNil(t, mockOm.FindGroup(om.TestGroupName))
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, util.OmGroupExternallyManagedTag)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.CreateGroup))
	mockOm.CheckOperationsDidntHappen(t, reflect.ValueOf(mockOm.UpdateGroup), reflect.ValueOf(mockOm.ReadOrganizations))
}

// TestPrepareOmConnection_CreateGroupFallback checks that if the group creation failed because tags editing is not allowed
// - the program failbacks to creating group without tags
func TestPrepareOmConnection_CreateGroupFallback(t *testing.T) {
	mockedOmConnection := omConnOldVersion()

	controller := newReconcileCommonController(newMockedManager(nil), mockedOmConnection)

	mockOm, vars := prepareConnection(controller, t)

	assert.Equal(t, om.TestGroupID, vars.ProjectID)
	assert.Equal(t, om.TestGroupID, mockOm.GroupID())
	assert.NotNil(t, mockOm.FindGroup(om.TestGroupName))
	assert.Empty(t, mockOm.FindGroup(om.TestGroupName).Tags)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.CreateGroup), reflect.ValueOf(mockOm.CreateGroup))
}

// TestPrepareOmConnection_CreateGroupFixTags fixes tags if they are not set for existing group
func TestPrepareOmConnection_CreateGroupFixTags(t *testing.T) {
	mockedOmConnection := omConnGroupWithoutTags()

	controller := newReconcileCommonController(newMockedManager(nil), mockedOmConnection)

	mockOm, _ := prepareConnection(controller, t)
	assert.Contains(t, mockOm.FindGroup(om.TestGroupName).Tags, util.OmGroupExternallyManagedTag)

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadGroups), reflect.ValueOf(mockOm.UpdateGroup))
}

// TestPrepareOmConnection_PrepareAgentKeys checks that agent key is generated and put to secret
func TestPrepareOmConnection_PrepareAgentKeys(t *testing.T) {
	manager := newMockedManager(nil)
	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)

	prepareConnection(controller, t)

	key, e := controller.kubeHelper.readAgentApiKeyForProject(TestNamespace, agentApiKeySecretName(om.TestGroupID))

	assert.NoError(t, e)
	// Unfortunately the key read is not equal to om.TestAgentKey - it's just some set of bytes.
	// This is reproduced only in mocked tests - the production is fine (the key is real string)
	// I assume that it's because when setting the secret data we use 'StringData' but read it back as
	// 'Data' which is binary. May be real kubernetes api reads data as string and updates
	assert.NotNil(t, key)

	manager.client.CheckOrderOfOperations(t,
		HItem(reflect.ValueOf(manager.client.Get), &v12.Secret{}),
		HItem(reflect.ValueOf(manager.client.Create), &v12.Secret{}))
}

// TestPrepareOmConnection_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
// map that allows to watch them for changes
func TestPrepareOmConnection_ConfigMapAndSecretWatched(t *testing.T) {
	manager := newMockedManager(nil)
	reconciler := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)

	// "create" a secret (config map already exists)
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mySecret", Namespace: "otherNs"},
		StringData: map[string]string{util.OmUser: "bla@mycompany.com", util.OmPublicApiKey: "2423423gdfgsdf23423sdfds"}}
	_ = manager.client.Create(context.TODO(), credentials)

	// Here we create two replica sets both referencing the same project and credentials
	vars := &PodVars{}
	spec := v1.CommonSpec{Project: TestProjectConfigMapName, Credentials: "otherNs/mySecret", LogLevel: v1.Warn}
	_, e := reconciler.prepareConnection(objectKey(TestNamespace, "ReplicaSetOne"), spec, vars, zap.S())
	assert.NoError(t, e)
	_, e = reconciler.prepareConnection(objectKey(TestNamespace, "ReplicaSetTwo"), spec, vars, zap.S())
	assert.NoError(t, e)

	// This one must not affect the map any way as everything is already registered
	_, e = reconciler.prepareConnection(objectKey(TestNamespace, "ReplicaSetTwo"), spec, vars, zap.S())
	assert.NoError(t, e)

	// we expect to have two entries in the map - each value has length of 2 meaning both replica sets are "registered"
	// to be reconciled as soon as config map or secret changes
	expected := map[watchedObject][]types.NamespacedName{
		watchedObject{resourceType: ConfigMap, resource: objectKey(TestNamespace, TestProjectConfigMapName)}: {objectKey(TestNamespace, "ReplicaSetOne"), objectKey(TestNamespace, "ReplicaSetTwo")},
		watchedObject{resourceType: Secret, resource: objectKey("otherNs", "mySecret")}:                      {objectKey(TestNamespace, "ReplicaSetOne"), objectKey(TestNamespace, "ReplicaSetTwo")},
	}
	assert.Equal(t, expected, reconciler.watchedResources)
}

// TestEnsureFinalizerHeaders checks that 'ensureFinalizerHeaders' function adds the finalizer header and updates the
// custom resource in K8s
func TestEnsureFinalizerHeaders(t *testing.T) {
	resource := DefaultStandaloneBuilder().Build()
	manager := newMockedManager(resource)
	assert.NotContains(t, resource.ObjectMeta.Finalizers, util.MongodbResourceFinalizer)

	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)
	assert.NoError(t, controller.ensureFinalizerHeaders(resource, &resource.ObjectMeta, zap.S()))

	assert.Contains(t, resource.ObjectMeta.Finalizers, util.MongodbResourceFinalizer)

	clientStandalone := &v1.MongoDbStandalone{}
	assert.NoError(t, manager.client.Get(context.TODO(), resource.ObjectKey(), clientStandalone))
	assert.Equal(t, resource, clientStandalone)

	// Duplicated call to 'ensureFinalizerHeaders' changes nothing
	assert.NoError(t, controller.ensureFinalizerHeaders(resource, &resource.ObjectMeta, zap.S()))
	assert.Contains(t, resource.ObjectMeta.Finalizers, util.MongodbResourceFinalizer)
}

// TestReconcileDeletion_Successful makes sure 'reconcileDeletion' function calls the passed function and removes the
// finalizer header
func TestReconcileDeletion_Successful(t *testing.T) {
	doReconcileDeletion(t, func() error { return nil })
}

// TestReconcileDeletion_Failed makes sure 'reconcileDeletion' function calls the passed function and removes the
// finalizer header and ignores the error during cleanup
func TestReconcileDeletion_Failed(t *testing.T) {
	doReconcileDeletion(t, func() error { return errors.New("FOO!!") })
}

func doReconcileDeletion(t *testing.T, f func() error) {
	resource := DefaultStandaloneBuilder().Build()
	manager := newMockedManager(resource)
	resource.ObjectMeta.Finalizers = append(resource.ObjectMeta.Finalizers, util.MongodbResourceFinalizer)

	controller := newReconcileCommonController(manager, om.NewEmptyMockedOmConnection)
	i := false
	result, e := controller.reconcileDeletion(
		func(obj interface{}, log *zap.SugaredLogger) error { i = true; return f() },
		resource,
		&resource.ObjectMeta,
		zap.S())

	assert.NoError(t, e)
	assert.Equal(t, reconcile.Result{}, result)
	assert.True(t, i) // cleanup function was called
	assert.NotContains(t, resource.ObjectMeta.Finalizers, util.MongodbResourceFinalizer)

	// Make sure client.client.Update was called for standalone and has no headers as well
	clientStandalone := &v1.MongoDbStandalone{}
	assert.NoError(t, manager.client.Get(context.TODO(), resource.ObjectKey(), clientStandalone))
	assert.Equal(t, resource, clientStandalone)
}

func prepareConnection(controller *ReconcileCommonController, t *testing.T) (*om.MockedOmConnection, *PodVars) {
	vars := &PodVars{}
	spec := v1.CommonSpec{Project: TestProjectConfigMapName, Credentials: TestCredentialsSecretName, LogLevel: v1.Warn}
	conn, e := controller.prepareConnection(objectKey(TestNamespace, ""), spec, vars, zap.S())
	mockOm := conn.(*om.MockedOmConnection)
	assert.NoError(t, e)
	return mockOm, vars
}

func omConnGroupWithoutTags() func(url, g, user, k string) om.Connection {
	return func(url, g, user, k string) om.Connection {
		c := om.NewEmptyMockedOmConnectionNoGroup(url, g, user, k).(*om.MockedOmConnection)
		if len(c.AllGroups) == 0 {
			// initially OM contains the group without tags
			c.AllGroups = []*om.Group{{Name: om.TestGroupName, ID: "123", AgentAPIKey: "12345abcd", OrgID: om.TestOrgID}}
			c.AllOrganizations = []*om.Organization{{ID: om.TestOrgID, Name: om.TestGroupName}}
		}

		return c
	}
}

func omConnGroupInOrganizationWithDifferentName() func(url, g, user, k string) om.Connection {
	return func(url, g, user, k string) om.Connection {
		c := om.NewEmptyMockedOmConnectionNoGroup(url, g, user, k).(*om.MockedOmConnection)
		if len(c.AllGroups) == 0 {
			// Important: the organization for the group has a different name ("foo") then group (om.TestGroupName).
			// So it won't work for cases when the group "was created before" by Operator
			c.AllGroups = []*om.Group{{Name: om.TestGroupName, ID: "existingGroupId", OrgID: om.TestOrgID}}
			c.AllOrganizations = []*om.Organization{{ID: om.TestOrgID, Name: "foo"}}
		}

		return c
	}
}

func omConnOldVersion() func(url, g, user, k string) om.Connection {
	cnt := 1
	return func(url, g, user, k string) om.Connection {
		c := om.NewEmptyMockedOmConnectionNoGroup(url, g, user, k).(*om.MockedOmConnection)
		c.CreateGroupFunc = func(g *om.Group) (*om.Group, error) {
			// first call
			if cnt == 1 {
				cnt++
				return nil, &om.APIError{ErrorCode: "INVALID_ATTRIBUTE", Detail: "Invalid attribute tags specified."}
			}
			// second call (fallback)
			g.ID = om.TestGroupID
			c.AllGroups = append(c.AllGroups, g)
			c.AllOrganizations = append(c.AllOrganizations, &om.Organization{ID: string(rand.Int()), Name: g.Name})
			return g, nil
		}
		// If creating tags is not allowed - then neither the update
		c.UpdateGroupFunc = func(g *om.Group) (*om.Group, error) {
			if len(g.Tags) > 0 {
				return nil, &om.APIError{ErrorCode: "INVALID_ATTRIBUTE", Detail: "Invalid attribute tags specified."}
			}
			return g, nil
		}
		return c
	}
}

func requestFromObject(object apiruntime.Object) reconcile.Request {
	return reconcile.Request{NamespacedName: objectKeyFromApiObject(object)}
}

func checkReconcileSuccessful(t *testing.T, reconciler reconcile.Reconciler, object v1.MongoDbResource, client *MockedClient) {
	result, e := reconciler.Reconcile(requestFromObject(object))
	assert.NoError(t, e)
	assert.Equal(t, reconcile.Result{}, result)

	// also need to make sure the object status is updated to successful
	assert.NoError(t, client.Get(context.TODO(), objectKeyFromApiObject(object), object))
	assert.Equal(t, v1.PhaseRunning, object.GetCommonStatus().Phase)

	expectedLink := DeploymentLink(om.TestURL, om.TestGroupID)
	switch s := object.(type) {
	case *v1.MongoDbStandalone:
		{
			assert.Equal(t, s.Spec.Version, s.Status.Version)
			assert.NotNil(t, s.Status.LastTransition)
			assert.NotEqual(t, s.Status.LastTransition, "")
			assert.Equal(t, expectedLink, s.Status.Link)

		}
	case *v1.MongoDbReplicaSet:
		{
			assert.Equal(t, s.Spec.Members, s.Status.Members)
			assert.Equal(t, s.Spec.Version, s.Status.Version)
			assert.NotNil(t, s.Status.LastTransition)
			assert.NotEqual(t, s.Status.LastTransition, "")
			assert.Equal(t, expectedLink, s.Status.Link)
		}
	case *v1.MongoDbShardedCluster:
		{
			assert.Equal(t, s.Spec.ConfigServerCount, s.Status.ConfigServerCount)
			assert.Equal(t, s.Spec.MongosCount, s.Status.MongosCount)
			assert.Equal(t, s.Spec.MongodsPerShardCount, s.Status.MongodsPerShardCount)
			assert.Equal(t, s.Spec.ShardCount, s.Status.ShardCount)
			assert.Equal(t, s.Spec.Version, s.Status.Version)
			assert.NotNil(t, s.Status.LastTransition)
			assert.NotEqual(t, s.Status.LastTransition, "")
			assert.Equal(t, expectedLink, s.Status.Link)
		}
	}
}

func checkReconcileFailed(t *testing.T, reconciler reconcile.Reconciler, object v1.MongoDbResource, expectedErrorMessage string, client *MockedClient) {
	failedResult := reconcile.Result{RequeueAfter: 10 * time.Second}
	result, e := reconciler.Reconcile(requestFromObject(object))
	assert.Nil(t, e, "When retrying, error should be nil")
	assert.Equal(t, failedResult, result)

	// also need to make sure the object status is updated to failed
	assert.NoError(t, client.Get(context.TODO(), objectKeyFromApiObject(object), object))
	assert.Equal(t, v1.PhaseFailed, object.GetCommonStatus().Phase)
	assert.Equal(t, expectedErrorMessage, object.GetCommonStatus().Message)
}
