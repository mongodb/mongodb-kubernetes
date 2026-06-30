package operator

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/v1/role"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/deployment"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connection"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/agentVersionManagement"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const OperatorNamespace = "operatorNs"

func init() {
	util.OperatorVersion = "9.9.9-test"
	_ = os.Setenv(util.CurrentNamespace, OperatorNamespace) // nolint:forbidigo
}

func TestEnsureTagAdded(t *testing.T) {
	ctx := context.Background()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
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
	opsManagerController := NewReconcileCommonController(ctx, kubeClient)

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

	controller := NewReconcileCommonController(ctx, kubeClient)
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
	controller := NewReconcileCommonController(ctx, kubeClient)

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

	controller := NewReconcileCommonController(ctx, kubeClient)

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

	controller := NewReconcileCommonController(ctx, kubeClient)

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
	controller := NewReconcileCommonController(ctx, kubeClient)

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
	controller := NewReconcileCommonController(ctx, kubeClient)
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

func TestFailWhenRoleAndRoleRefsAreConfigured(t *testing.T) {
	ctx := context.Background()
	customRole := mdbv1.MongoDBRole{
		Role:                       "foo",
		AuthenticationRestrictions: []mdbv1.AuthenticationRestriction{},
		Db:                         "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "readWriteAnyDatabase",
		}},
	}
	roleResource := role.DefaultClusterMongoDBRoleBuilder().Build()
	roleRef := mdbv1.MongoDBRoleRef{
		Name: roleResource.Name,
		Kind: util.ClusterMongoDBRoleKind,
	}
	assert.Nil(t, customRole.Privileges)
	rs := mdbv1.NewDefaultReplicaSetBuilder().SetRoles([]mdbv1.MongoDBRole{customRole}).SetRoleRefs([]mdbv1.MongoDBRoleRef{roleRef}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	result := controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())
	assert.False(t, result.IsOK())
	assert.Equal(t, status.PhaseFailed, result.Phase())

	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDBRole)
	assert.False(t, ok)
	assert.Empty(t, roles)
}

func TestRoleRefsAreAdded(t *testing.T) {
	ctx := context.Background()
	roleResource := role.DefaultClusterMongoDBRoleBuilder().Build()
	roleRefs := []mdbv1.MongoDBRoleRef{
		{
			Name: roleResource.Name,
			Kind: util.ClusterMongoDBRoleKind,
		},
	}
	rs := mdbv1.NewDefaultReplicaSetBuilder().SetRoleRefs(roleRefs).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	_ = kubeClient.Create(ctx, roleResource)

	controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())

	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDBRole)
	assert.True(t, ok)
	assert.NotNil(t, roles[0].Privileges)
	assert.Len(t, roles, 1)
}

func TestErrorWhenRoleRefIsWrong(t *testing.T) {
	ctx := context.Background()
	roleResource := role.DefaultClusterMongoDBRoleBuilder().Build()
	roleRefs := []mdbv1.MongoDBRoleRef{
		{
			Name: roleResource.Name,
			Kind: "WrongMongoDBRoleReference",
		},
	}
	rs := mdbv1.NewDefaultReplicaSetBuilder().SetRoleRefs(roleRefs).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	_ = kubeClient.Create(ctx, roleResource)

	result := controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())
	assert.False(t, result.IsOK())
	assert.Equal(t, status.PhaseFailed, result.Phase())

	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDBRole)
	assert.False(t, ok)
	assert.Empty(t, roles)
}

func TestErrorWhenRoleDoesNotExist(t *testing.T) {
	ctx := context.Background()
	roleResource := role.DefaultClusterMongoDBRoleBuilder().Build()
	roleRefs := []mdbv1.MongoDBRoleRef{
		{
			Name: roleResource.Name,
			Kind: util.ClusterMongoDBRoleKind,
		},
	}
	rs := mdbv1.NewDefaultReplicaSetBuilder().SetRoleRefs(roleRefs).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	result := controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())
	assert.False(t, result.IsOK())
	assert.Equal(t, status.PhaseFailed, result.Phase())

	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDBRole)
	assert.False(t, ok)
	assert.Empty(t, roles)
}

func TestDontSendNilPrivileges(t *testing.T) {
	ctx := context.Background()
	customRole := mdbv1.MongoDBRole{
		Role:                       "foo",
		AuthenticationRestrictions: []mdbv1.AuthenticationRestriction{},
		Db:                         "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "readWriteAnyDatabase",
		}},
	}
	assert.Nil(t, customRole.Privileges)
	rs := DefaultReplicaSetBuilder().SetRoles([]mdbv1.MongoDBRole{customRole}).Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)
	controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())
	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDBRole)
	assert.True(t, ok)
	assert.NotNil(t, roles[0].Privileges)
}

func TestCheckEmptyStringsInPrivilegesEquivalentToNotPassingFields(t *testing.T) {
	ctx := context.Background()

	roleWithEmptyStrings := mdbv1.MongoDBRole{
		Role: "withEmptyStrings",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "read",
		}},
		Privileges: []mdbv1.Privilege{
			{
				Resource: mdbv1.Resource{
					Db:         ptr.To("config"),
					Collection: ptr.To(""), // Explicit empty string
				},
				Actions: []string{"find", "update", "insert", "remove"},
			},
			{
				Resource: mdbv1.Resource{
					Db:         ptr.To("users"),
					Collection: ptr.To("usersCollection"),
				},
				Actions: []string{"update", "insert", "remove"},
			},
			{
				Resource: mdbv1.Resource{
					Db:         ptr.To(""), // Explicit empty string
					Collection: ptr.To(""), // Explicit empty string
				},
				Actions: []string{"find"},
			},
			{
				Resource: mdbv1.Resource{
					Cluster: ptr.To(true),
				},
				Actions: []string{"find"},
			},
			{
				Resource: mdbv1.Resource{
					Cluster:    ptr.To(true),
					Db:         ptr.To(""),
					Collection: ptr.To(""),
				},
				Actions: []string{"find"},
			},
		},
	}

	// Role without empty strings (fields omitted, which should result in empty strings for string types)
	roleWithoutEmptyStrings := mdbv1.MongoDBRole{
		Role: "withoutEmptyFields",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "read",
		}},
		Privileges: []mdbv1.Privilege{
			{
				Resource: mdbv1.Resource{
					Db: ptr.To("config"),
					// field not set, should pass ""
				},
				Actions: []string{"find", "update", "insert", "remove"},
			},
			{
				Resource: mdbv1.Resource{
					Db:         ptr.To("users"),
					Collection: ptr.To("usersCollection"),
				},
				Actions: []string{"update", "insert", "remove"},
			},
			{
				Resource: mdbv1.Resource{
					// fields not set, should be passed as empty strings
				},
				Actions: []string{"find"},
			},
			{
				Resource: mdbv1.Resource{
					Cluster: ptr.To(true),
				},
				Actions: []string{"find"},
			},
		},
	}

	rs := DefaultReplicaSetBuilder().SetRoles([]mdbv1.MongoDBRole{roleWithEmptyStrings, roleWithoutEmptyStrings}).Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())

	ac, err := mockOm.ReadAutomationConfig()
	assert.NoError(t, err)
	roles, ok := ac.Deployment["roles"].([]mdbv1.MongoDBRole)
	assert.True(t, ok)
	require.Len(t, roles, 2)

	// we iterate over two created privileges because both should end with the same result
	for i := range 2 {
		assert.Nil(t, roles[i].Privileges[0].Resource.Cluster)
		assert.Equal(t, ptr.To("config"), roles[i].Privileges[0].Resource.Db)
		// even if the db or collection field is not passed it must result in empty string
		assert.Equal(t, ptr.To(""), roles[i].Privileges[0].Resource.Collection)

		assert.Nil(t, roles[i].Privileges[1].Resource.Cluster)
		assert.Equal(t, ptr.To("users"), roles[i].Privileges[1].Resource.Db)
		assert.Equal(t, ptr.To("usersCollection"), roles[i].Privileges[1].Resource.Collection)

		assert.Nil(t, roles[i].Privileges[2].Resource.Cluster)
		assert.Equal(t, ptr.To(""), roles[i].Privileges[2].Resource.Db)
		assert.Equal(t, ptr.To(""), roles[i].Privileges[2].Resource.Collection)

		require.NotNil(t, roles[i].Privileges[3].Resource.Cluster)
		assert.True(t, *roles[i].Privileges[3].Resource.Cluster)
		assert.Nil(t, roles[i].Privileges[3].Resource.Db)
		assert.Nil(t, roles[i].Privileges[3].Resource.Collection)
	}
}

func TestMergeRoles(t *testing.T) {
	externalRole := mdbv1.MongoDBRole{
		Role: "ext_role",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "read",
		}},
	}
	role1 := mdbv1.MongoDBRole{
		Role: "role1",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "readWrite",
		}},
	}

	role2 := mdbv1.MongoDBRole{
		Role: "role2",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "readWrite",
		}},
	}

	tests := []struct {
		name          string
		deployedRoles []mdbv1.MongoDBRole
		currentRoles  []mdbv1.MongoDBRole
		previousRoles []string
		expectedRoles []mdbv1.MongoDBRole
	}{
		// externalRole was added via UI
		// role1 and role2 were defined in the CR
		// role2 was removed from the CR
		{
			name:          "Removing role from resource",
			deployedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
			currentRoles:  []mdbv1.MongoDBRole{role1},
			previousRoles: []string{"role1@admin", "role2@admin"},
			expectedRoles: []mdbv1.MongoDBRole{externalRole, role1},
		},
		// externalRole was added via UI
		// role1 was defined in the CR
		// role2 was added in the CR
		{
			name:          "Adding role in resource",
			deployedRoles: []mdbv1.MongoDBRole{externalRole, role1},
			currentRoles:  []mdbv1.MongoDBRole{role1, role2},
			previousRoles: []string{"role1@admin"},
			expectedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
		},
		{
			name:          "Idempotency",
			deployedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
			currentRoles:  []mdbv1.MongoDBRole{role1, role2},
			previousRoles: []string{"role1@admin", "role2@admin"},
			expectedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
		},
		{
			name:          "Nil previous roles - adding all defined roles",
			deployedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
			currentRoles:  []mdbv1.MongoDBRole{role1, role2},
			previousRoles: nil,
			expectedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
		},
		{
			name:          "Nil current roles - removing all defined roles",
			deployedRoles: []mdbv1.MongoDBRole{externalRole, role1, role2},
			currentRoles:  nil,
			previousRoles: []string{"role1@admin", "role2@admin"},
			expectedRoles: []mdbv1.MongoDBRole{externalRole},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mergedRoles := mergeRoles(tc.deployedRoles, tc.currentRoles, tc.previousRoles)

			require.Len(t, mergedRoles, len(tc.expectedRoles))
			for _, r := range tc.expectedRoles {
				assert.Contains(t, mergedRoles, r)
			}
		})
	}
}

func TestExternalRoleIsNotRemoved(t *testing.T) {
	ctx := context.Background()

	role := mdbv1.MongoDBRole{
		Role: "embedded-role",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "read",
		}},
	}

	rs := DefaultReplicaSetBuilder().SetRoles([]mdbv1.MongoDBRole{role}).Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient()
	controller := NewReconcileCommonController(ctx, kubeClient)
	mockOm, _ := prepareConnection(ctx, controller, omConnectionFactory.GetConnectionFunc, t)

	// Create deployment with one embedded role
	controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), nil, zap.S())

	roles := mockOm.GetRoles()
	require.Len(t, roles, 1)

	// Add external role directly to OM (via UI/API)
	externalRole := mdbv1.MongoDBRole{
		Role: "external-role",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{{
			Db:   "admin",
			Role: "read",
		}},
	}
	mockOm.AddRole(externalRole)

	// Ensure external role is added
	roles = mockOm.GetRoles()
	require.Len(t, roles, 2)

	// Reconcile again - role created from the UI should still be there
	roleStrings, _ := controller.getRoleStrings(ctx, rs.Spec.DbCommonSpec, true, kube.ObjectKeyFromApiObject(rs))
	controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), roleStrings, zap.S())

	roles = mockOm.GetRoles()
	require.Len(t, roles, 2)

	// Delete embedded role, only the external should remain
	rs.Spec.Security.Roles = nil
	controller.ensureRoles(ctx, rs.Spec.DbCommonSpec, true, mockOm, kube.ObjectKeyFromApiObject(rs), roleStrings, zap.S())

	roles = mockOm.GetRoles()
	require.Len(t, roles, 1)
	assert.Equal(t, roles[0].Role, "external-role")
}

// TestSetupCommonWatchers_NilTLSConfig_WithCertificatesSecretsPrefix tests that SetupCommonWatchers
// handles the case when CertificatesSecretsPrefix is set but TLSConfig is nil.
// This tests the fix for CLOUDP-352133: IsTLSEnabled() returns true when
// CertificatesSecretsPrefix is set, and the code must handle nil TLSConfig gracefully.
func TestSetupCommonWatchers_NilTLSConfig_WithCertificatesSecretsPrefix(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()
	// Set CertificatesSecretsPrefix but leave TLSConfig nil
	// IsTLSEnabled() will return true because CertificatesSecretsPrefix != ""
	// The code should handle nil TLSConfig gracefully without panicking
	rs.Spec.Security = &mdbv1.Security{
		CertificatesSecretsPrefix: "my-prefix",
		// TLSConfig is intentionally nil
	}

	kubeClient, _ := mock.NewDefaultFakeClient(rs)
	controller := NewReconcileCommonController(ctx, kubeClient)

	assert.NotPanics(t, func() {
		controller.SetupCommonWatchers(rs, nil, nil, rs.Name)
	}, "SetupCommonWatchers should not panic when CertificatesSecretsPrefix is set but TLSConfig is nil")
}

func TestSecretWatcherWithAllResources(t *testing.T) {
	ctx := context.Background()
	caName := "custom-ca"
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().SetTLSCA(caName).Build()
	rs.Spec.Security.Authentication.InternalCluster = "X509"
	kubeClient, _ := mock.NewDefaultFakeClient(rs)
	controller := NewReconcileCommonController(ctx, kubeClient)

	controller.SetupCommonWatchers(rs, nil, nil, rs.Name)

	// TODO: unify the watcher setup with the secret creation/mounting code in database creation
	memberCert := rs.GetSecurity().MemberCertificateSecretName(rs.Name)
	internalAuthCert := rs.GetSecurity().InternalClusterAuthSecretName(rs.Name)
	agentCert := rs.GetSecurity().AgentClientCertificateSecretName(rs.Name)

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
	controller := NewReconcileCommonController(ctx, kubeClient)

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

func TestAgentCertHashAndPath(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tls-secret",
				Namespace: mock.TestNamespace,
			},
			StringData: map[string]string{
				corev1.TLSCertKey: "fake-contents",
			},
			Type: corev1.SecretTypeTLS,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "opaque-secret",
				Namespace: mock.TestNamespace,
			},
			StringData: map[string]string{
				corev1.TLSCertKey: "fake-contents",
			},
			Type: corev1.SecretTypeOpaque,
		},
	)
	controller := NewReconcileCommonController(ctx, kubeClient)

	tests := []struct {
		name         string
		secretName   string
		expectedHash string
		expectedPath string
	}{
		{
			name:         "TLS secret",
			secretName:   "tls-secret",
			expectedHash: "IQJW7I2VWNTYUEKGVULPP2DET2KPWT6CD7TX5AYQYBQPMHFK76FA",
			expectedPath: "/mongodb-automation/agent-certs/IQJW7I2VWNTYUEKGVULPP2DET2KPWT6CD7TX5AYQYBQPMHFK76FA",
		},
		{
			name:         "Opaque secret",
			secretName:   "opaque-secret",
			expectedHash: "",
			expectedPath: "",
		},
		{
			name:         "Secret doesn't exist",
			secretName:   "non-existent-secret",
			expectedHash: "",
			expectedPath: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hash, path := controller.agentCertHashAndPath(ctx, zap.S(), mock.TestNamespace, tc.secretName, "")
			assert.Equal(t, tc.expectedHash, hash)
			assert.Equal(t, tc.expectedPath, path)
		})
	}
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
	expected, _ := workflow.Pending("doesn't matter").Requeue().ReconcileResult()
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

	// also need to make sure the object status is pending
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
	for _, object := range objects {
		wg.Add(1)
		go func() {
			result, err := reconciler.Reconcile(ctx, requestFromObject(object))
			assert.NoError(t, err)

			// Reconcile again if it's OpsManager, it has to configure AppDB in second run
			if _, ok := object.(*omv1.MongoDBOpsManager); ok {
				result, err = reconciler.Reconcile(ctx, requestFromObject(object))
				assert.NoError(t, err)
			}
			assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)

			// Idempotent reconcile that does not mutate anything, but makes sure we have better code coverage
			result, err = reconciler.Reconcile(ctx, requestFromObject(object))
			assert.NoError(t, err)
			assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)

			wg.Done()
		}()
	}
	wg.Wait()
}

func testFCVsCases(t *testing.T, verifyFCV func(version string, expectedFCV string, fcvOverride *string, t *testing.T)) {
	// Define the test cases in order. They need to be in order!
	testCases := []struct {
		version     string
		expectedFCV string
		fcvOverride *string
	}{
		{"4.0.0", "4.0", nil},
		{"5.0.0", "4.0", nil},
		{"5.0.0", "4.0", nil},
		{"6.0.0", "6.0", nil},
		{"7.0.0", "7.0", ptr.To("AlwaysMatchVersion")},
		{"8.0.0", "8.0", nil},
		{"7.0.0", "7.0", nil},
	}

	// Iterate through the test cases in order
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Version=%s", tc.version), func(t *testing.T) {
			verifyFCV(tc.version, tc.expectedFCV, tc.fcvOverride, t)
		})
	}
}

// ---------------------------------------------------------------------------
// Package-level helpers shared by Tasks 8–11
// ---------------------------------------------------------------------------

func buildRsByProcessesHelper(rsName string, processes []om.Process) om.ReplicaSetWithProcesses {
	options := make([]automationconfig.MemberOptions, len(processes))
	return om.NewReplicaSetWithProcesses(
		om.NewReplicaSet(rsName, "6.0.0"),
		processes,
		options,
		nil,
	)
}

func createRSProcessesHelper(rsName string, count int) []om.Process {
	ps := make([]om.Process, count)
	for i := 0; i < count; i++ {
		spec := &mdbv1.MongoDbSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "6.0.0"}}
		ps[i] = om.NewMongodProcess(
			fmt.Sprintf("%s-%d", rsName, i),
			fmt.Sprintf("%s-%d.some.host", rsName, i),
			"fake-image", false,
			&mdbv1.AdditionalMongodConfig{},
			spec, "", nil, "",
		)
	}
	return ps
}

type failingOMConn struct {
	om.Connection
}

func (f failingOMConn) ReadDeployment() (om.Deployment, error) {
	return om.NewDeployment(), fmt.Errorf("forced error")
}

// ---------------------------------------------------------------------------
// Task 8: checkExternalMembersDrift
// ---------------------------------------------------------------------------

func TestCheckExternalMembersDrift_EmptyList(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	status := checkExternalMembersDrift(conn, nil)
	assert.True(t, status.IsOK())
}

func TestCheckExternalMembersDrift_MissingProcessInAC(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	externalMembers := []mdbv1.ExternalMember{
		{ProcessName: "not-in-ac", Hostname: "not-in-ac:27017", Type: "mongod"},
	}
	status := checkExternalMembersDrift(conn, externalMembers)
	assert.False(t, status.IsOK())
}

func TestCheckExternalMembersDrift_MatchingProcess(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 1))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())

	conn := om.NewMockedOmConnection(d)
	externalMembers := []mdbv1.ExternalMember{
		{ProcessName: "my-rs-0", Hostname: "my-rs-0.some.host:27017", Type: "mongod", ReplicaSetName: "my-rs"},
	}
	status := checkExternalMembersDrift(conn, externalMembers)
	assert.True(t, status.IsOK())
}

func TestCheckExternalMembersDrift_HostnameMismatch(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 1))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())

	conn := om.NewMockedOmConnection(d)
	externalMembers := []mdbv1.ExternalMember{
		{ProcessName: "my-rs-0", Hostname: "wrong-host:27017", Type: "mongod", ReplicaSetName: "my-rs"},
	}
	status := checkExternalMembersDrift(conn, externalMembers)
	assert.False(t, status.IsOK())
}

// ---------------------------------------------------------------------------
// Task 9: validateACForMigration
// ---------------------------------------------------------------------------

func TestValidateACForMigration_EmptyList(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 0, nil)
	status := validateACForMigration(conn, mdb)
	assert.True(t, status.IsOK())
}

func TestValidateACForMigration_TLSModeSet(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 1))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())
	d.GetProcesses()[0].EnsureNetConfig()["tls"] = map[string]interface{}{"mode": "requireTLS"}

	conn := om.NewMockedOmConnection(d)
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 1, []mdbv1.ExternalMember{
		{ProcessName: "my-rs-0", Hostname: "my-rs-0.some.host:27017", Type: "mongod"},
	})
	status := validateACForMigration(conn, mdb)
	assert.True(t, status.IsOK())
}

func TestValidateACForMigration_TLSModeNotSet(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 1))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())
	// NewMongodProcess sets tls.mode="disabled" by default; delete the key entirely
	// so net.tls.mode is absent, validateACForMigration rejects absent TLS, not just "disabled"
	delete(d.GetProcesses()[0].EnsureNetConfig(), "tls")

	conn := om.NewMockedOmConnection(d)
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 1, []mdbv1.ExternalMember{
		{ProcessName: "my-rs-0", Hostname: "my-rs-0.some.host:27017", Type: "mongod"},
	})
	status := validateACForMigration(conn, mdb)
	assert.False(t, status.IsOK())
}

func TestValidateACForMigration_ReadDeploymentError(t *testing.T) {
	conn := failingOMConn{om.NewMockedOmConnection(om.NewDeployment())}
	mdb := mongoDBForMigrationTest("some-rs", "my-ns", 1, []mdbv1.ExternalMember{
		{ProcessName: "some-proc", Hostname: "some-proc:27017", Type: "mongod"},
	})
	status := validateACForMigration(conn, mdb)
	assert.False(t, status.IsOK())
}

// statusMsg extracts the error message from a workflow.Status via StatusOptions.
func statusMsg(st workflow.Status) string {
	opt, exists := status.GetOption(st.StatusOptions(), status.MessageOption{})
	if !exists {
		return ""
	}
	return opt.(status.MessageOption).Message
}

func TestValidateACForMigration_BoundarySeven_OK(t *testing.T) {
	// 3 K8s voting + 4 voting external = 7, at boundary, should pass
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 3, fourExternalMembers())
	conn := newMigrationACConn(t, mdb, fourVotingExternals(), 3)

	st := validateACForMigration(conn, mdb)
	assert.True(t, st.IsOK(), "expected OK, got: %+v", st)
}

func TestValidateACForMigration_ScaleUpExceedsLimit(t *testing.T) {
	// AC has 3 K8s voting + 4 voting external = 7 (at the limit). User scales spec.Members
	// from 3 to 4 → position 3 defaults to votes=1, pushing the total to 8. Newly voting = [3].
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 4, fourExternalMembers())
	conn := newMigrationACConn(t, mdb, fourVotingExternals(), 3)

	st := validateACForMigration(conn, mdb)
	require.False(t, st.IsOK())

	// Lock the whole error message: format, ordering, leading whitespace, all of it.
	expectedMsg := `"my-rs": this reconcile would result in 8 voting members (max: 7).
Currently voting in the Automation Config (7):
  1. ext-0 (external)
  2. ext-1 (external)
  3. ext-2 (external)
  4. ext-3 (external)
  5. k8s/my-ns/my-rs-0 (Kubernetes)
  6. k8s/my-ns/my-rs-1 (Kubernetes)
  7. k8s/my-ns/my-rs-2 (Kubernetes)
This reconcile would make the following Kubernetes member(s) voting:
  - spec.memberConfig[3]
To fix: revert 1 of the above memberConfig entries to votes=0 and priority="0".
If you wish to make more of the kubernetes members voting, make sure to remove one of the voting external members in the list above.`
	assert.Equal(t, expectedMsg, statusMsg(st))
}

func TestValidateACForMigration_TwoNewVotingPositionsExceedLimit(t *testing.T) {
	// AC has 3 K8s voting + 4 voting external = 7. User scales to 5 K8s → newly voting [3, 4].
	// Post-reconcile = 9, excess = 2.
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 5, fourExternalMembers())
	conn := newMigrationACConn(t, mdb, fourVotingExternals(), 3)

	st := validateACForMigration(conn, mdb)
	require.False(t, st.IsOK())

	expectedMsg := `"my-rs": this reconcile would result in 9 voting members (max: 7).
Currently voting in the Automation Config (7):
  1. ext-0 (external)
  2. ext-1 (external)
  3. ext-2 (external)
  4. ext-3 (external)
  5. k8s/my-ns/my-rs-0 (Kubernetes)
  6. k8s/my-ns/my-rs-1 (Kubernetes)
  7. k8s/my-ns/my-rs-2 (Kubernetes)
This reconcile would make the following Kubernetes member(s) voting:
  - spec.memberConfig[3]
  - spec.memberConfig[4]
To fix: revert 2 of the above memberConfig entries to votes=0 and priority="0".
If you wish to make more of the kubernetes members voting, make sure to remove one of the voting external members in the list above.`
	assert.Equal(t, expectedMsg, statusMsg(st))
}

func TestValidateACForMigration_MemberConfigFlipExceedsLimit(t *testing.T) {
	// AC has 5 K8s voting (positions 0..4) + 2 voting external = 7. AC also has K8s position 5
	// existing but non-voting. User sets spec.MemberConfig[5].votes=1. Post-reconcile = 6 K8s +
	// 2 ext = 8. Newly voting = [5]; excess = 1.
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 6, fourExternalMembers())
	v1 := 1
	p1 := "1"
	mdb.Spec.MemberConfig = []automationconfig.MemberOptions{
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1}, // user flipped position 5 from 0 → 1
	}

	// 2 voting externals + 2 non-voting externals; 6 K8s with position 5 non-voting in AC.
	externals := []om.ReplicaSetMember{
		externalRSMember("ext-0", 1), externalRSMember("ext-1", 1),
		externalRSMember("ext-2", 0), externalRSMember("ext-3", 0),
	}
	conn := newMigrationACConnWithK8sVotes(t, mdb, externals, 6, []int{1, 1, 1, 1, 1, 0})

	st := validateACForMigration(conn, mdb)
	require.False(t, st.IsOK())

	// Non-voting externals (ext-2, ext-3) and non-voting K8s (position 5) are absent from the
	// AC voting list. Only the single flipped position (5) is listed under "would make voting".
	expectedMsg := `"my-rs": this reconcile would result in 8 voting members (max: 7).
Currently voting in the Automation Config (7):
  1. ext-0 (external)
  2. ext-1 (external)
  3. k8s/my-ns/my-rs-0 (Kubernetes)
  4. k8s/my-ns/my-rs-1 (Kubernetes)
  5. k8s/my-ns/my-rs-2 (Kubernetes)
  6. k8s/my-ns/my-rs-3 (Kubernetes)
  7. k8s/my-ns/my-rs-4 (Kubernetes)
This reconcile would make the following Kubernetes member(s) voting:
  - spec.memberConfig[5]
To fix: revert 1 of the above memberConfig entries to votes=0 and priority="0".
If you wish to make more of the kubernetes members voting, make sure to remove one of the voting external members in the list above.`
	assert.Equal(t, expectedMsg, statusMsg(st))
}

func TestValidateACForMigration_NonVotingExternals_DoNotCount(t *testing.T) {
	// 5 K8s voting + 3 non-voting externals = 5 voting → OK
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 5, []mdbv1.ExternalMember{
		{ProcessName: "ext-0", Hostname: "ext-0:27017", Type: "mongod"},
		{ProcessName: "ext-1", Hostname: "ext-1:27017", Type: "mongod"},
		{ProcessName: "ext-2", Hostname: "ext-2:27017", Type: "mongod"},
	})
	conn := newMigrationACConn(t, mdb, []om.ReplicaSetMember{
		externalRSMember("ext-0", 0), externalRSMember("ext-1", 0), externalRSMember("ext-2", 0),
	}, 5)

	st := validateACForMigration(conn, mdb)
	assert.True(t, st.IsOK(), "expected OK, got: %+v", st)
}

func TestValidateACForMigration_NonVotingK8sMembersViaConfig_NotCounted(t *testing.T) {
	// 3 voting K8s (positions 0-2 via MemberConfig) + 2 non-voting K8s (positions 3-4)
	// + 4 voting external = 7 → OK
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 5, fourExternalMembers())
	v0 := 0
	v1 := 1
	p0 := "0"
	p1 := "1"
	mdb.Spec.MemberConfig = []automationconfig.MemberOptions{
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1},
		{Votes: &v1, Priority: &p1},
		{Votes: &v0, Priority: &p0},
		{Votes: &v0, Priority: &p0},
	}
	conn := newMigrationACConn(t, mdb, fourVotingExternals(), 5)

	st := validateACForMigration(conn, mdb)
	assert.True(t, st.IsOK(), "expected OK, got: %+v", st)
}

// ---------------------------------------------------------------------------
// Helpers for validateACForMigration tests
// ---------------------------------------------------------------------------

// mongoDBForMigrationTest builds a minimal *mdbv1.MongoDB suitable for the migration validator.
// The validator only reads Name, Namespace, and Spec.{Members,MemberConfig,ExternalMembers,
// ResourceType}, so we leave everything else at zero values.
func mongoDBForMigrationTest(name, namespace string, members int, externalMembers []mdbv1.ExternalMember) *mdbv1.MongoDB {
	return &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec:    mdbv1.DbCommonSpec{ResourceType: mdbv1.ReplicaSet},
			Members:         members,
			ExternalMembers: externalMembers,
		},
	}
}

// newMigrationACConn builds an Ops Manager connection whose deployment has the migration-shaped
// replica set: externalMembers first (with low _ids 0..N-1) followed by k8sCount K8s members
// (with _ids starting at len(externalMembers)). K8s member names use the k8s/<namespace>/...
// naming scheme, which matches the names computePostReconcileVoting expects to find. TLS mode is
// set on the first process so the TLS check passes. By default each K8s member is voting.
func newMigrationACConn(t *testing.T, mdb *mdbv1.MongoDB, externalMembers []om.ReplicaSetMember, k8sCount int) om.Connection {
	t.Helper()
	k8sVotes := make([]int, k8sCount)
	for i := range k8sVotes {
		k8sVotes[i] = 1
	}
	return newMigrationACConnWithK8sVotes(t, mdb, externalMembers, k8sCount, k8sVotes)
}

// newMigrationACConnWithK8sVotes is like newMigrationACConn but lets the caller specify per-K8s
// member votes in the AC. len(k8sVotes) must equal k8sCount.
func newMigrationACConnWithK8sVotes(t *testing.T, mdb *mdbv1.MongoDB, externalMembers []om.ReplicaSetMember, k8sCount int, k8sVotes []int) om.Connection {
	t.Helper()
	require.Equal(t, k8sCount, len(k8sVotes), "k8sVotes must have one entry per K8s member")

	d := om.NewDeployment()
	rsName := mdb.GetReplicaSetName()
	// Build K8s processes with the prefixed names the validator computes.
	rs := buildRsByProcessesHelper(rsName, createK8sProcessesForMigrationTest(rsName, mdb.Namespace, k8sCount))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())
	d.GetProcesses()[0].EnsureNetConfig()["tls"] = map[string]interface{}{"mode": "requireTLS"}

	// Apply per-K8s voting state (default 1; tests may set 0 for non-voting).
	acRS := d.GetReplicaSetByName(rsName)
	for i, m := range acRS.Members() {
		m["votes"] = k8sVotes[i]
		if k8sVotes[i] == 0 {
			m["priority"] = float32(0)
		}
	}

	// Prepend external members at the start of the RS member list with low _ids, shifting
	// existing K8s members' _ids upward to match production behaviour (externals are pre-existing
	// in OM, K8s members get _ids starting from MAX(external _id) + 1).
	prependExternalMembersForTest(d, rsName, externalMembers...)

	return om.NewMockedOmConnection(d)
}

// createK8sProcessesForMigrationTest builds K8s processes whose Process.Name() equals
// process.PodNameToProcessName(dns.GetPodName(rsName, i), namespace), i.e. "k8s/<ns>/<rs>-<i>".
// This matches the name format computePostReconcileVoting expects when looking up K8s members in
// the AC.
func createK8sProcessesForMigrationTest(rsName, namespace string, count int) []om.Process {
	ps := make([]om.Process, count)
	for i := 0; i < count; i++ {
		spec := &mdbv1.MongoDbSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "6.0.0"}}
		processName := process.PodNameToProcessName(dns.GetPodName(rsName, i), namespace)
		ps[i] = om.NewMongodProcess(
			processName,
			fmt.Sprintf("%s-%d.some.host", rsName, i),
			"fake-image", false,
			&mdbv1.AdditionalMongodConfig{},
			spec, "", nil, "",
		)
	}
	return ps
}

func externalRSMember(host string, votes int) om.ReplicaSetMember {
	return om.ReplicaSetMember{
		"_id":      0, // overwritten by prependExternalMembersForTest
		"host":     host,
		"votes":    votes,
		"priority": float32(1),
	}
}

// prependExternalMembersForTest inserts external members at the START of the RS member list,
// shifting existing (K8s) members' _ids upward. Matches production where externals are
// pre-existing in OM with low _ids and K8s members get higher _ids assigned afterwards.
func prependExternalMembersForTest(d om.Deployment, rsName string, externalMembers ...om.ReplicaSetMember) {
	rs := d.GetReplicaSetByName(rsName)
	existing := rs.Members()
	newMembers := make([]om.ReplicaSetMember, 0, len(externalMembers)+len(existing))
	for i, m := range externalMembers {
		m["_id"] = i
		newMembers = append(newMembers, m)
	}
	for i, m := range existing {
		m["_id"] = len(externalMembers) + i
		newMembers = append(newMembers, m)
	}
	rs["members"] = newMembers
}

// fourExternalMembers returns four canonical spec.ExternalMember entries (ext-0..ext-3).
func fourExternalMembers() []mdbv1.ExternalMember {
	return []mdbv1.ExternalMember{
		{ProcessName: "ext-0", Hostname: "ext-0:27017", Type: "mongod"},
		{ProcessName: "ext-1", Hostname: "ext-1:27017", Type: "mongod"},
		{ProcessName: "ext-2", Hostname: "ext-2:27017", Type: "mongod"},
		{ProcessName: "ext-3", Hostname: "ext-3:27017", Type: "mongod"},
	}
}

// fourVotingExternals returns four AC RS members named ext-0..ext-3 with votes=1.
func fourVotingExternals() []om.ReplicaSetMember {
	return []om.ReplicaSetMember{
		externalRSMember("ext-0", 1), externalRSMember("ext-1", 1),
		externalRSMember("ext-2", 1), externalRSMember("ext-3", 1),
	}
}

func TestCheckExternalMembersDrift_ShardedMongosProcess(t *testing.T) {
	d := om.NewDeployment()
	configRs := om.NewReplicaSetWithProcesses(
		om.NewReplicaSet("myCluster-config", "6.0.0"),
		createRSProcessesHelper("myCluster-config", 1),
		[]automationconfig.MemberOptions{{}},
		nil,
	)
	mongosProc := om.NewMongosProcess(
		"myCluster-mongos-0", "myCluster-mongos-0.some.host",
		"fake-image", false,
		&mdbv1.AdditionalMongodConfig{},
		&mdbv1.MongoDbSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "6.0.0"}},
		"", nil, "",
	)
	_, err := d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            "myCluster",
		MongosProcesses: []om.Process{mongosProc},
		ConfigServerRs:  configRs,
		Shards:          []om.ReplicaSetWithProcesses{},
	})
	require.NoError(t, err)

	conn := om.NewMockedOmConnection(d)
	externalMembers := []mdbv1.ExternalMember{
		{ProcessName: "myCluster-mongos-0", Hostname: "myCluster-mongos-0.some.host:27017", Type: "mongos"},
	}
	status := checkExternalMembersDrift(conn, externalMembers)
	assert.True(t, status.IsOK())
}

func TestCheckExternalMembersDrift_ShardedMongodWithReplicaSetName(t *testing.T) {
	d := om.NewDeployment()
	configRs := om.NewReplicaSetWithProcesses(
		om.NewReplicaSet("myCluster-config", "6.0.0"),
		createRSProcessesHelper("myCluster-config", 1),
		[]automationconfig.MemberOptions{{}},
		nil,
	)
	_, err := d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            "myCluster",
		MongosProcesses: []om.Process{},
		ConfigServerRs:  configRs,
		Shards:          []om.ReplicaSetWithProcesses{},
	})
	require.NoError(t, err)

	conn := om.NewMockedOmConnection(d)
	externalMembers := []mdbv1.ExternalMember{
		{ProcessName: "myCluster-config-0", Hostname: "myCluster-config-0.some.host:27017", Type: "mongod", ReplicaSetName: "myCluster-config"},
	}
	status := checkExternalMembersDrift(conn, externalMembers)
	assert.True(t, status.IsOK())
}

func TestCheckExternalMembersDrift_ShardedMongodWrongReplicaSetName(t *testing.T) {
	d := om.NewDeployment()
	configRs := om.NewReplicaSetWithProcesses(
		om.NewReplicaSet("myCluster-config", "6.0.0"),
		createRSProcessesHelper("myCluster-config", 1),
		[]automationconfig.MemberOptions{{}},
		nil,
	)
	_, err := d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            "myCluster",
		MongosProcesses: []om.Process{},
		ConfigServerRs:  configRs,
		Shards:          []om.ReplicaSetWithProcesses{},
	})
	require.NoError(t, err)

	conn := om.NewMockedOmConnection(d)
	externalMembers := []mdbv1.ExternalMember{
		{ProcessName: "myCluster-config-0", Hostname: "myCluster-config-0.some.host:27017", Type: "mongod", ReplicaSetName: "wrong-rs"},
	}
	status := checkExternalMembersDrift(conn, externalMembers)
	assert.False(t, status.IsOK())
}

func TestValidateACForMigration_ShardedCluster_TLSModeSet(t *testing.T) {
	d := om.NewDeployment()
	configRs := om.NewReplicaSetWithProcesses(
		om.NewReplicaSet("myCluster-config", "6.0.0"),
		createRSProcessesHelper("myCluster-config", 1),
		[]automationconfig.MemberOptions{{}},
		nil,
	)
	mongosProc := om.NewMongosProcess(
		"myCluster-mongos-0", "myCluster-mongos-0.some.host",
		"fake-image", false,
		&mdbv1.AdditionalMongodConfig{},
		&mdbv1.MongoDbSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "6.0.0"}},
		"", nil, "",
	)
	_, err := d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            "myCluster",
		MongosProcesses: []om.Process{mongosProc},
		ConfigServerRs:  configRs,
		Shards:          []om.ReplicaSetWithProcesses{},
	})
	require.NoError(t, err)

	// Both process types have net.tls.mode set.
	for _, p := range d.GetProcesses() {
		p.EnsureNetConfig()["tls"] = map[string]interface{}{"mode": "disabled"}
	}

	conn := om.NewMockedOmConnection(d)
	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "myCluster"},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{ResourceType: mdbv1.ShardedCluster},
			ExternalMembers: []mdbv1.ExternalMember{
				{ProcessName: "myCluster-mongos-0", Hostname: "myCluster-mongos-0.some.host:27017", Type: "mongos"},
				{ProcessName: "myCluster-config-0", Hostname: "myCluster-config-0.some.host:27017", Type: "mongod", ReplicaSetName: "myCluster-config"},
			},
		},
	}
	status := validateACForMigration(conn, mdb)
	assert.True(t, status.IsOK())
}

func TestValidateVotingLimitSharded_ConfigServerExceedsLimit(t *testing.T) {
	// 3 voting external config server members + 5 K8s config server members = 8 → error.
	sc := newShardedMDBForVotingTest("myCluster", 5, 1)
	d := buildShardedDeploymentForVotingTest(t, sc, 3)
	st := validateVotingLimitSharded(sc, d)
	require.False(t, st.IsOK())
	msg := statusMsg(st)
	assert.Contains(t, msg, "8 voting members")
	assert.Contains(t, msg, "myCluster-config")
}

func TestValidateVotingLimitSharded_ConfigServerUnderLimit(t *testing.T) {
	// 3 voting external + 3 K8s = 6 → OK.
	sc := newShardedMDBForVotingTest("myCluster", 3, 1)
	d := buildShardedDeploymentForVotingTest(t, sc, 3)
	st := validateVotingLimitSharded(sc, d)
	assert.True(t, st.IsOK())
}

func TestValidateVotingLimitSharded_ConfigServerNonVotingK8s(t *testing.T) {
	// 5 external voting + 5 K8s all non-voting via MemberConfig = 5 → OK.
	v0 := 0
	p0 := "0"
	sc := newShardedMDBForVotingTest("myCluster", 5, 1)
	sc.Spec.MemberConfig = make([]automationconfig.MemberOptions, 5)
	for i := range sc.Spec.MemberConfig {
		sc.Spec.MemberConfig[i] = automationconfig.MemberOptions{Votes: &v0, Priority: &p0}
	}
	d := buildShardedDeploymentForVotingTest(t, sc, 5)
	st := validateVotingLimitSharded(sc, d)
	assert.True(t, st.IsOK())
}

func TestValidateVotingLimitSharded_ShardExceedsLimit(t *testing.T) {
	// Shard 0: 3 voting external + 5 K8s = 8 → error.
	sc := newShardedMDBForVotingTest("myCluster", 1, 5)
	sc.Spec.ExternalMembers = append(sc.Spec.ExternalMembers,
		mdbv1.ExternalMember{ProcessName: "myCluster-0-0", Hostname: "shard-0:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
		mdbv1.ExternalMember{ProcessName: "myCluster-0-1", Hostname: "shard-1:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
		mdbv1.ExternalMember{ProcessName: "myCluster-0-2", Hostname: "shard-2:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
	)
	d := buildShardedDeploymentForVotingTest(t, sc, 3)
	st := validateVotingLimitSharded(sc, d)
	require.False(t, st.IsOK())
	msg := statusMsg(st)
	assert.Contains(t, msg, "8 voting members")
	assert.Contains(t, msg, "myCluster-0")
}

func TestValidateVotingLimitSharded_MongosNotCounted(t *testing.T) {
	// Mongos external members must not contribute to RS voting counts.
	sc := newShardedMDBForVotingTest("myCluster", 3, 1)
	for i := range sc.Spec.ExternalMembers {
		if sc.Spec.ExternalMembers[i].Type == "mongod" {
			sc.Spec.ExternalMembers[i].Type = "mongos"
			sc.Spec.ExternalMembers[i].ReplicaSetName = ""
		}
	}
	d := buildShardedDeploymentForVotingTest(t, sc, 0)
	st := validateVotingLimitSharded(sc, d)
	assert.True(t, st.IsOK())
}

// newShardedMDBForVotingTest builds a minimal sharded cluster MongoDB with configServerCount K8s
// config server members and mongodsPerShard K8s shard members. External members are 3 voting
// config server mongods.
func newShardedMDBForVotingTest(name string, configServerCount, mongodsPerShard int) *mdbv1.MongoDB {
	return &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{ResourceType: mdbv1.ShardedCluster},
			MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{
				ShardCount:           1,
				MongodsPerShardCount: mongodsPerShard,
				ConfigServerCount:    configServerCount,
			},
			ExternalMembers: []mdbv1.ExternalMember{
				{ProcessName: name + "-config-0", Hostname: "cfg-0:27017", Type: "mongod", ReplicaSetName: name + "-config"},
				{ProcessName: name + "-config-1", Hostname: "cfg-1:27017", Type: "mongod", ReplicaSetName: name + "-config"},
				{ProcessName: name + "-config-2", Hostname: "cfg-2:27017", Type: "mongod", ReplicaSetName: name + "-config"},
			},
		},
	}
}

// buildShardedDeploymentForVotingTest creates an OM deployment with a config server RS containing
// configVotingExternal voting external members and a shard RS containing shardVotingExternal voting
// external members. TLS mode is set so the TLS check passes.
func buildShardedDeploymentForVotingTest(t *testing.T, sc *mdbv1.MongoDB, shardVotingExternal int) om.Deployment {
	t.Helper()
	configRsName := sc.ConfigRsName()
	shardRsName := sc.ShardName(0)

	configExternalProcs := createRSProcessesHelper(configRsName, 3)
	configRS := om.NewReplicaSetWithProcesses(
		om.NewReplicaSet(configRsName, "6.0.0"),
		configExternalProcs,
		[]automationconfig.MemberOptions{{}, {}, {}},
		nil,
	)

	shardExternalProcs := createRSProcessesHelper(shardRsName, shardVotingExternal)
	shardOpts := make([]automationconfig.MemberOptions, shardVotingExternal)
	for i := range shardOpts {
		shardOpts[i] = automationconfig.MemberOptions{}
	}
	shardRS := om.NewReplicaSetWithProcesses(
		om.NewReplicaSet(shardRsName, "6.0.0"),
		shardExternalProcs,
		shardOpts,
		nil,
	)

	d := om.NewDeployment()
	_, err := d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            sc.Name,
		ConfigServerRs:  configRS,
		Shards:          []om.ReplicaSetWithProcesses{shardRS},
		MongosProcesses: []om.Process{},
	})
	require.NoError(t, err)

	for _, p := range d.GetProcesses() {
		p.EnsureNetConfig()["tls"] = map[string]interface{}{"mode": "requireTLS"}
	}
	return d
}

// ---------------------------------------------------------------------------
// Task 10: checkIfHasExcessProcesses
// ---------------------------------------------------------------------------

func TestCheckIfHasExcessProcesses_ReadDeploymentError(t *testing.T) {
	conn := failingOMConn{om.NewMockedOmConnection(om.NewDeployment())}
	status := checkIfHasExcessProcesses(conn, "my-rs", nil, zap.S())
	assert.False(t, status.IsOK())
}

func TestCheckIfHasExcessProcesses_SingleResource(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 2))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())

	conn := om.NewMockedOmConnection(d)
	status := checkIfHasExcessProcesses(conn, "my-rs", nil, zap.S())
	assert.True(t, status.IsOK())
}

func TestCheckIfHasExcessProcesses_MultipleResources(t *testing.T) {
	d := om.NewDeployment()
	rs1 := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 1))
	rs2 := buildRsByProcessesHelper("other-rs", createRSProcessesHelper("other-rs", 1))
	d.MergeReplicaSet(rs1, nil, nil, nil, zap.S())
	d.MergeReplicaSet(rs2, nil, nil, nil, zap.S())

	conn := om.NewMockedOmConnection(d)
	status := checkIfHasExcessProcesses(conn, "my-rs", nil, zap.S())
	assert.False(t, status.IsOK())
}

// ---------------------------------------------------------------------------
// Task 11: getReplicaSetProcessIdsFromReplicaSets
// ---------------------------------------------------------------------------

func TestGetReplicaSetProcessIdsFromReplicaSets_NotFound(t *testing.T) {
	d := om.NewDeployment()
	result := getReplicaSetProcessIdsFromReplicaSets("nonexistent-rs", d)
	assert.Empty(t, result)
}

func TestGetReplicaSetProcessIdsFromReplicaSets_Found(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("my-rs", createRSProcessesHelper("my-rs", 3))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())

	result := getReplicaSetProcessIdsFromReplicaSets("my-rs", d)

	assert.Len(t, result, 3)
	assert.Contains(t, result, "my-rs-0")
	assert.Contains(t, result, "my-rs-1")
	assert.Contains(t, result, "my-rs-2")
	assert.Equal(t, 0, result["my-rs-0"])
	assert.Equal(t, 1, result["my-rs-1"])
	assert.Equal(t, 2, result["my-rs-2"])
}

// TestValidateVotingLimitSharded_ShardNonVotingK8sViaMemberConfig verifies spec.memberConfig is
// honoured for shard members in single cluster topology.
func TestValidateVotingLimitSharded_ShardNonVotingK8sViaMemberConfig(t *testing.T) {
	v0 := 0
	p0 := "0"
	sc := newShardedMDBForVotingTest("myCluster", 1, 5)
	sc.Spec.ExternalMembers = append(sc.Spec.ExternalMembers,
		mdbv1.ExternalMember{ProcessName: "myCluster-0-0", Hostname: "shard-0:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
		mdbv1.ExternalMember{ProcessName: "myCluster-0-1", Hostname: "shard-1:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
		mdbv1.ExternalMember{ProcessName: "myCluster-0-2", Hostname: "shard-2:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
	)
	sc.Spec.MemberConfig = make([]automationconfig.MemberOptions, 5)
	for i := range sc.Spec.MemberConfig {
		sc.Spec.MemberConfig[i] = automationconfig.MemberOptions{Votes: &v0, Priority: &p0}
	}
	d := buildShardedDeploymentForVotingTest(t, sc, 3)
	st := validateVotingLimitSharded(sc, d)
	assert.True(t, st.IsOK())
}

// TestValidateVotingLimitSharded_ShardNonVotingK8sViaShardOverride verifies that shardOverrides
// memberConfig is honoured for shard members in single cluster topology.
func TestValidateVotingLimitSharded_ShardNonVotingK8sViaShardOverride(t *testing.T) {
	v0 := 0
	p0 := "0"
	sc := newShardedMDBForVotingTest("myCluster", 1, 5)
	sc.Spec.ExternalMembers = append(sc.Spec.ExternalMembers,
		mdbv1.ExternalMember{ProcessName: "myCluster-0-0", Hostname: "shard-0:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
		mdbv1.ExternalMember{ProcessName: "myCluster-0-1", Hostname: "shard-1:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
		mdbv1.ExternalMember{ProcessName: "myCluster-0-2", Hostname: "shard-2:27017", Type: "mongod", ReplicaSetName: "myCluster-0"},
	)
	overrideMemberConfig := make([]automationconfig.MemberOptions, 5)
	for i := range overrideMemberConfig {
		overrideMemberConfig[i] = automationconfig.MemberOptions{Votes: &v0, Priority: &p0}
	}
	sc.Spec.ShardOverrides = []mdbv1.ShardOverride{{ShardNames: []string{"myCluster-0"}, MemberConfig: overrideMemberConfig}}
	d := buildShardedDeploymentForVotingTest(t, sc, 3)
	st := validateVotingLimitSharded(sc, d)
	assert.True(t, st.IsOK())
}

// TestValidateShardedACIdentity verifies that the resolved AC names are required to match the
// existing sharded cluster in the AC during migration.
func TestValidateShardedACIdentity(t *testing.T) {
	sc := newShardedMDBForVotingTest("myCluster", 1, 1)
	d := buildShardedDeploymentForVotingTest(t, sc, 1)

	assert.True(t, validateShardedACIdentity(sc, d).IsOK())

	wrongClusterName := sc.DeepCopy()
	wrongClusterName.Spec.ShardedClusterNameOverride = "other-cluster"
	st := validateShardedACIdentity(wrongClusterName, d)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "does not contain a sharded cluster named other-cluster")

	wrongConfigName := sc.DeepCopy()
	wrongConfigName.Spec.ConfigServerNameOverride = "vm-config"
	st = validateShardedACIdentity(wrongConfigName, d)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "config server replica set")

	wrongShardRs := sc.DeepCopy()
	wrongShardRs.Spec.ShardNameOverrides = []mdbv1.ShardNameOverride{{ShardName: "myCluster-0", ShardId: "vm-0", ReplicaSetName: "vm-0"}}
	st = validateShardedACIdentity(wrongShardRs, d)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "no shard with replica set name vm-0")

	// The merge matches shards by _id, so a correct replicaSetName paired with a wrong shardId is rejected.
	wrongShardId := sc.DeepCopy()
	wrongShardId.Spec.ShardNameOverrides = []mdbv1.ShardNameOverride{{ShardName: "myCluster-0", ShardId: "vm-id", ReplicaSetName: "myCluster-0"}}
	st = validateShardedACIdentity(wrongShardId, d)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "spec.shardNameOverrides specifies shardId vm-id")
}

// TestValidateShardedACIdentity_UncoveredACShard verifies that an AC shard the resource does not
// resolve to is rejected instead of being silently drained by the merge.
func TestValidateShardedACIdentity_UncoveredACShard(t *testing.T) {
	sc := newShardedMDBForVotingTest("myCluster", 1, 1)

	d := om.NewDeployment()
	d["sharding"] = []om.ShardedCluster{{
		"name":                sc.Name,
		"configServerReplica": sc.ConfigRsName(),
		"shards": []om.Shard{
			{"_id": "myCluster-0", "rs": "myCluster-0"},
			{"_id": "vm-extra", "rs": "vm-extra"},
		},
	}}
	st := validateShardedACIdentity(sc, d)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "vm-extra")
	assert.Contains(t, statusMsg(st), "does not cover")

	// A covered replica set name whose AC _id differs from the resolved _id is rejected as well.
	d["sharding"] = []om.ShardedCluster{{
		"name":                sc.Name,
		"configServerReplica": sc.ConfigRsName(),
		"shards":              []om.Shard{{"_id": "weird-id", "rs": "myCluster-0"}},
	}}
	st = validateShardedACIdentity(sc, d)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "the resource resolves to _id myCluster-0")
}

// TestValidateRSACIdentity verifies the AC must contain a replica set under the resolved name during migration.
func TestValidateRSACIdentity(t *testing.T) {
	d := om.NewDeployment()
	rs := buildRsByProcessesHelper("vm-rs", createRSProcessesHelper("vm-rs", 3))
	d.MergeReplicaSet(rs, nil, nil, nil, zap.S())

	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-rs"},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec:           mdbv1.DbCommonSpec{ResourceType: mdbv1.ReplicaSet},
			ReplicaSetNameOverride: "vm-rs",
		},
	}
	acRs, st := validateRSACIdentity(mdb, d)
	assert.True(t, st.IsOK())
	require.NotNil(t, acRs)
	assert.Equal(t, "vm-rs", acRs.Name())

	mdb.Spec.ReplicaSetNameOverride = ""
	acRs, st = validateRSACIdentity(mdb, d)
	require.False(t, st.IsOK())
	assert.Nil(t, acRs)
	assert.Contains(t, statusMsg(st), "does not contain a replica set named my-rs")
}

// TestValidateVotingLimitRS exercises the voting limit check directly with the looked up replica set.
func TestValidateVotingLimitRS(t *testing.T) {
	mdb := mongoDBForMigrationTest("my-rs", "my-ns", 3, fourExternalMembers())
	conn := newMigrationACConn(t, mdb, fourVotingExternals(), 3)
	deployment, err := conn.ReadDeployment()
	require.NoError(t, err)
	rs := deployment.GetReplicaSetByName(mdb.GetReplicaSetName())
	require.NotNil(t, rs)

	// 3 K8s voting plus 4 voting external members is exactly at the limit.
	assert.True(t, validateVotingLimitRS(mdb, rs).IsOK())

	// Scaling spec members to 4 makes position 3 voting and pushes the total to 8.
	mdb.Spec.Members = 4
	st := validateVotingLimitRS(mdb, rs)
	require.False(t, st.IsOK())
	assert.Contains(t, statusMsg(st), "8 voting members")
}
