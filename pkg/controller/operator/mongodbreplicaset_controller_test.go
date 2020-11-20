package operator

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"
	"github.com/google/uuid"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type ReplicaSetBuilder struct {
	*mdbv1.MongoDB
}

func TestCreateReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	assert.Len(t, client.GetMapForObject(&corev1.Service{}), 1)
	assert.Len(t, client.GetMapForObject(&appsv1.StatefulSet{}), 1)
	assert.Equal(t, *client.GetSet(rs.ObjectKey()).Spec.Replicas, int32(3))
	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromReplicaSet(rs), "auth", "ssl")
	connection.CheckNumberOfUpdateRequests(t, 1)
}

func TestHorizonVerificationTLS(t *testing.T) {
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
		{"my-horizon": "my-db.com:12347"},
	}
	rs := DefaultReplicaSetBuilder().SetReplicaSetHorizons(replicaSetHorizons).Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	msg := "TLS must be enabled in order to use replica set horizons"
	checkReconcileFailed(t, reconciler, rs, false, msg, client)
}

func TestHorizonVerificationCount(t *testing.T) {
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
	}
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		SetReplicaSetHorizons(replicaSetHorizons).
		Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	msg := "Number of horizons must be equal to number of members in replica set"
	checkReconcileFailed(t, reconciler, rs, false, msg, client)
}

// TestScaleUpReplicaSet verifies scaling up for replica set. Statefulset and OM Deployment must be changed accordingly
func TestScaleUpReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetMembers(3).Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcileSuccessful(t, reconciler, rs, client)
	set := &appsv1.StatefulSet{}
	_ = client.Get(context.TODO(), mock.ObjectKeyFromApiObject(rs), set)

	// Now scale up to 5 nodes
	rs = DefaultReplicaSetBuilder().SetMembers(5).Build()
	_ = client.Update(context.TODO(), rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	updatedSet := &appsv1.StatefulSet{}
	_ = client.Get(context.TODO(), mock.ObjectKeyFromApiObject(rs), updatedSet)

	// Statefulset is expected to be the same - only number of replicas changed
	set.Spec.Replicas = util.Int32Ref(int32(5))
	assert.Equal(t, set.Spec, updatedSet.Spec)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromReplicaSet(rs), "auth", "ssl")
	connection.CheckNumberOfUpdateRequests(t, 2)
}

func TestCreateReplicaSet_TLS(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetMembers(3).EnableTLS().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcilePending(t, reconciler, rs, "Not all certificates have been approved by Kubernetes CA for temple", client, 10)
	client.ApproveAllCSRs()
	checkReconcileSuccessful(t, reconciler, rs, client)

	processes := om.CurrMockedConnection.GetProcesses()
	assert.Len(t, processes, 3)
	for _, v := range processes {
		assert.NotNil(t, v.SSLConfig())
		assert.Len(t, v.SSLConfig(), 2)
		assert.Equal(t, util.PEMKeyFilePathInContainer, v.SSLConfig()["PEMKeyFile"])
		assert.Equal(t, "requireSSL", v.SSLConfig()["mode"])
	}

	sslConfig := om.CurrMockedConnection.GetSSL()
	assert.Equal(t, util.CAFilePathInContainer, sslConfig["CAFilePath"])
	assert.Equal(t, "OPTIONAL", sslConfig["clientCertificateMode"])
}

// TestCreateDeleteReplicaSet checks that no state is left in OpsManager on removal of the replicaset
func TestCreateDeleteReplicaSet(t *testing.T) {
	// First we need to create a replicaset
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcileSuccessful(t, reconciler, rs, client)
	omConn := om.CurrMockedConnection
	omConn.CleanHistory()

	// Now delete it
	assert.NoError(t, reconciler.delete(rs, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

func TestX509IsNotEnabledWithOlderVersionsOfOpsManager(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableAuth().EnableTLS().SetAuthModes([]string{util.X509}).Build()
	reconciler, client := defaultReplicaSetReconciler(rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		conn := om.NewEmptyMockedOmConnection(context)

		// make the mocked connection return an error behaving as an older version of Ops Manager
		conn.(*om.MockedOmConnection).UpdateMonitoringAgentConfigFunc = func(mac *om.MonitoringAgentConfig, log *zap.SugaredLogger) (bytes []byte, e error) {
			return nil, fmt.Errorf("some error. Detail: %s", util.MethodNotAllowed)
		}
		return conn
	}

	addKubernetesTlsResources(client, rs)
	approveAgentCSRs(client, 1)

	checkReconcileFailed(t, reconciler, rs, true, "unable to configure X509 with this version of Ops Manager", client)
}

func TestReplicaSetScramUpgradeDowngrade(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetVersion("4.0.0").EnableAuth().SetAuthModes([]string{"SCRAM"}).Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(authentication.ScramSha256))

	// downgrade to version that will not use SCRAM-SHA-256
	rs.Spec.Version = "3.6.9"

	_ = client.Update(context.TODO(), rs)

	checkReconcileFailed(t, reconciler, rs, false, "Unable to downgrade to SCRAM-SHA-1 when SCRAM-SHA-256 has been enabled", client)
}

func TestReplicaSetCustomPodSpecTemplate(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().SetPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeName: "some-node-name",
			Hostname: "some-host-name",
			Containers: []corev1.Container{{
				Name:  "my-custom-container",
				Image: "my-custom-image",
				VolumeMounts: []corev1.VolumeMount{{
					Name: "my-volume-mount",
				}},
			}},
			RestartPolicy: corev1.RestartPolicyAlways,
		},
	}).Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	addKubernetesTlsResources(client, rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	// read the stateful set that was created by the operator
	statefulSet := getStatefulSet(client, mock.ObjectKeyFromApiObject(rs))

	assertPodSpecSts(t, statefulSet)

	podSpecTemplate := statefulSet.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container", podSpecTemplate.Containers[1].Name, "Custom container should be second")
}

func TestFeatureControlPolicyAndTagAddedWithNewerOpsManager(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "4.2.2",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, rs, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 2)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)

	project := mockedConn.FindGroup("my-project")
	assert.Contains(t, project.Tags, util.OmGroupExternallyManagedTag)
}

func TestFeatureControlPolicyNoAuthNewerOpsManager(t *testing.T) {
	rsBuilder := DefaultReplicaSetBuilder()
	rsBuilder.Spec.Security = nil

	rs := rsBuilder.Build()

	reconciler, client := defaultReplicaSetReconciler(rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "4.2.2",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, rs, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 1)
	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)
}

func TestOnlyTagIsAppliedToOlderOpsManager(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "4.2.1",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, rs, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	// no feature controls are configured
	assert.Empty(t, cf.Policies)
	assert.Empty(t, cf.ManagementSystem.Version)
	assert.Empty(t, cf.ManagementSystem.Name)

	project := mockedConn.FindGroup("my-project")
	assert.Contains(t, project.Tags, util.OmGroupExternallyManagedTag)
}

func TestScalingScalesOneMemberAtATime_WhenScalingDown(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetMembers(5).Build()
	reconciler, client := defaultReplicaSetReconciler(rs)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(t, reconciler, rs, client)

	// scale down from 5 to 3 members
	rs.Spec.Members = 3

	err := client.Update(context.TODO(), rs)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(requestFromObject(rs))

	assert.NoError(t, err)
	assert.Equal(t, time.Duration(10000000000), res.RequeueAfter, "Scaling from 5 -> 4 should enqueue another reconciliation")

	assertCorrectNumberOfMembersAndProcesses(t, 4, rs, client, "We should have updated the status with the intermediate value of 4")

	res, err = reconciler.Reconcile(requestFromObject(rs))
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), res.RequeueAfter, "Once we reach the target value, we should not scale anymore")

	assertCorrectNumberOfMembersAndProcesses(t, 3, rs, client, "The members should now be set to the final desired value")

}

func TestScalingScalesOneMemberAtATime_WhenScalingUp(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetMembers(1).Build()
	reconciler, client := defaultReplicaSetReconciler(rs)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(t, reconciler, rs, client)

	// scale up from 1 to 3 members
	rs.Spec.Members = 3

	err := client.Update(context.TODO(), rs)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(requestFromObject(rs))
	assert.NoError(t, err)

	assert.Equal(t, time.Duration(10000000000), res.RequeueAfter, "Scaling from 1 -> 3 should enqueue another reconciliation")

	assertCorrectNumberOfMembersAndProcesses(t, 2, rs, client, "We should have updated the status with the intermediate value of 2")

	res, err = reconciler.Reconcile(requestFromObject(rs))
	assert.NoError(t, err)

	assertCorrectNumberOfMembersAndProcesses(t, 3, rs, client, "Once we reach the target value, we should not scale anymore")
}

func TestReplicaSetPortIsConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	config := mdbv1.NewAdditionalMongodConfig("net.port", 30000)
	rs := mdbv1.NewReplicaSetBuilder().
		SetNamespace(mock.TestNamespace).
		SetAdditionalConfig(config).
		SetConnectionSpec(testConnectionSpec()).
		Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	svc, err := client.GetService(kube.ObjectKey(rs.Namespace, rs.ServiceName()))
	assert.NoError(t, err)
	assert.Equal(t, int32(30000), svc.Spec.Ports[0].Port)
}

func TestReplicaSetSettingDeprecatedFieldsAddsWarning(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		Build()
	reconciler, client := defaultReplicaSetReconciler(rs)

	t.Run("Warnings should be added to status if using shortcut resources", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: rs.Name, Namespace: rs.Namespace}, rs)
		assert.NoError(t, err)
		rs.Spec.PodSpec.Cpu = "1"

		err = client.Update(context.TODO(), rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, rs, client)

		assert.NotEmpty(t, rs.Status.Warnings)
		assert.Subset(t, rs.Status.Warnings, []status.Warning{mdbv1.UseOfDeprecatedShortcutFieldsWarning})
	})

	t.Run("Checks Warnings are removed when removing shortcut resources", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: rs.Name, Namespace: rs.Namespace}, rs)
		assert.NoError(t, err)

		rs.Spec.PodSpec.Cpu = ""

		err = client.Update(context.TODO(), rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, rs, client)
		assert.Empty(t, rs.Status.Warnings)
	})

	t.Run("A new shortcut resource, will add a new warning", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: rs.Name, Namespace: rs.Namespace}, rs)
		assert.NoError(t, err)

		rs.Spec.PodSpec.Memory = "1"
		err = client.Update(context.TODO(), rs)
		assert.NoError(t, err)

		// Reconciles and makes sure we get a warning
		checkReconcileSuccessful(t, reconciler, rs, client)
		assert.NotEmpty(t, rs.Status.Warnings)
		assert.Subset(t, rs.Status.Warnings, []status.Warning{mdbv1.UseOfDeprecatedShortcutFieldsWarning})
	})

	t.Run("The shortcut resource is removed a new one is added, warnings stay", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: rs.Name, Namespace: rs.Namespace}, rs)
		assert.NoError(t, err)

		rs.Spec.PodSpec.Memory = ""
		rs.Spec.PodSpec.Cpu = "1"

		err = client.Update(context.TODO(), rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, rs, client)
		assert.NotEmpty(t, rs.Status.Warnings)
		assert.Subset(t, rs.Status.Warnings, []status.Warning{mdbv1.UseOfDeprecatedShortcutFieldsWarning})
	})
}

//TestReplicaSet_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
//map that allows to watch them for changes
func TestReplicaSet_ConfigMapAndSecretWatched(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, rs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, rs.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, rs.Name)},
	}

	assert.Equal(t, reconciler.WatchedResources, expected)
}

func TestBackupConfiguration_ReplicaSet(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		SetBackup(mdbv1.Backup{
			Mode:       "enabled",
		}).
		Build()

	reconciler, client := defaultReplicaSetReconciler(rs)

	uuidStr := uuid.New().String()
	// configure backup for this project in Ops Manager in the mocked connection
	om.CurrMockedConnection = om.NewMockedOmConnection(om.NewDeployment())
	om.CurrMockedConnection.UpdateBackupConfig(&backup.Config{
		ClusterId: uuidStr,
		Status:    backup.Inactive,
	})

	t.Run("Backup can be started", func(t *testing.T) {
		checkReconcileSuccessful(t, reconciler, rs, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be stopped", func(t *testing.T) {
		rs.Spec.Backup.Mode = "disabled"
		err := client.Update(context.TODO(), rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, rs, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		rs.Spec.Backup.Mode = "terminated"
		err := client.Update(context.TODO(), rs)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, rs, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

}

// assertCorrectNumberOfMembersAndProcesses ensures that both the mongodb resource and the Ops Manager deployment
// have the correct number of processes/replicas at each stage of the scaling operation
func assertCorrectNumberOfMembersAndProcesses(t *testing.T, expected int, mdb *mdbv1.MongoDB, client *mock.MockedClient, msg string) {
	err := client.Get(context.TODO(), mdb.ObjectKey(), mdb)
	assert.NoError(t, err)
	assert.Equal(t, expected, mdb.Status.Members, msg)
	dep, err := om.CurrMockedConnection.ReadDeployment()
	assert.NoError(t, err)
	assert.Len(t, dep.ProcessesCopy(), expected)
}

// defaultReplicaSetReconciler is the replica set reconciler used in unit test. It "adds" necessary
// additional K8s objects (rs, connection config map and secrets) necessary for reconciliation
// so it's possible to call 'reconcile()' on it right away
func defaultReplicaSetReconciler(rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, *mock.MockedClient) {
	return replicaSetReconcilerWithConnection(rs, om.NewEmptyMockedOmConnection)
}

func replicaSetReconcilerWithConnection(rs *mdbv1.MongoDB, connectionFunc func(ctx *om.OMContext) om.Connection) (*ReconcileMongoDbReplicaSet, *mock.MockedClient) {
	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()

	return newReplicaSetReconciler(manager, connectionFunc), manager.Client
}

// TODO remove in favor of '/api/mongodbbuilder.go'
func DefaultReplicaSetBuilder() *ReplicaSetBuilder {
	podSpec := NewDefaultPodSpec()
	spec := mdbv1.MongoDbSpec{
		Version:    "4.0.0",
		Persistent: util.BooleanRef(false),
		ConnectionSpec: mdbv1.ConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{
					Name: mock.TestProjectConfigMapName,
				},
			},
			Credentials: mock.TestCredentialsSecretName,
		},
		ResourceType: mdbv1.ReplicaSet,
		Members:      3,
		PodSpec:      &podSpec,
		Security: &mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
			Roles: []mdbv1.MongoDbRole{},
		},
	}
	rs := &mdbv1.MongoDB{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: mock.TestNamespace}}
	return &ReplicaSetBuilder{rs}
}

func (b *ReplicaSetBuilder) SetClusterAuth(auth string) *ReplicaSetBuilder {
	b.Spec.Security.ClusterAuthMode = auth
	return b
}
func (b *ReplicaSetBuilder) SetName(name string) *ReplicaSetBuilder {
	b.Name = name
	return b
}
func (b *ReplicaSetBuilder) SetVersion(version string) *ReplicaSetBuilder {
	b.Spec.Version = version
	return b
}
func (b *ReplicaSetBuilder) SetPersistent(p *bool) *ReplicaSetBuilder {
	b.Spec.Persistent = p
	return b
}
func (b *ReplicaSetBuilder) SetMembers(m int) *ReplicaSetBuilder {
	b.Spec.Members = m
	return b
}

func (b *ReplicaSetBuilder) SetSecurity(security mdbv1.Security) *ReplicaSetBuilder {
	b.Spec.Security = &security
	return b
}

func (b *ReplicaSetBuilder) SetAuthentication(auth *mdbv1.Authentication) *ReplicaSetBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}
	b.Spec.Security.Authentication = auth
	return b
}

func (b *ReplicaSetBuilder) SetRoles(roles []mdbv1.MongoDbRole) *ReplicaSetBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}
	b.Spec.Security.Roles = roles
	return b
}

func (b *ReplicaSetBuilder) EnableAuth() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	return b
}

func (b *ReplicaSetBuilder) AgentAuthMode(agentMode string) *ReplicaSetBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}

	if b.Spec.Security.Authentication == nil {
		b.Spec.Security.Authentication = &mdbv1.Authentication{}
	}
	b.Spec.Security.Authentication.Agents = mdbv1.AgentAuthentication{Mode: agentMode}
	return b
}

func (b *ReplicaSetBuilder) LDAP(ldap mdbv1.Ldap) *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Ldap = &ldap
	return b
}

func (b *ReplicaSetBuilder) SetAuthModes(modes []string) *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *ReplicaSetBuilder) EnableX509InternalClusterAuth() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.InternalCluster = util.X509
	return b
}

func (b *ReplicaSetBuilder) SetReplicaSetHorizons(replicaSetHorizons []mdbv1.MongoDBHorizonConfig) *ReplicaSetBuilder {
	if b.Spec.Connectivity == nil {
		b.Spec.Connectivity = &mdbv1.MongoDBConnectivity{}
	}
	b.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	return b
}

func (b *ReplicaSetBuilder) EnableTLS() *ReplicaSetBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		b.SetSecurity(mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{}})
	}
	b.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *ReplicaSetBuilder) EnableX509() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.X509)
	return b
}

func (b *ReplicaSetBuilder) EnableSCRAM() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.SCRAM)
	return b
}

func (b *ReplicaSetBuilder) EnableLDAP() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.LDAP)
	return b
}

func (b *ReplicaSetBuilder) SetPodSpecTemplate(spec corev1.PodTemplateSpec) *ReplicaSetBuilder {
	if b.Spec.PodSpec == nil {
		b.Spec.PodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.PodSpec.PodTemplate = &spec
	return b
}

func (b *ReplicaSetBuilder) Build() *mdbv1.MongoDB {
	b.InitDefaults()
	return b.MongoDB.DeepCopy()
}

func createDeploymentFromReplicaSet(rs *mdbv1.MongoDB) om.Deployment {
	helper := createStatefulHelperFromReplicaSet(rs)

	sts, _ := helper.BuildStatefulSet()
	d := om.NewDeployment()
	d.MergeReplicaSet(
		buildReplicaSetFromStatefulSet(sts, rs),
		nil,
	)
	d.AddMonitoringAndBackup(zap.S(), rs.Spec.GetTLSConfig().IsEnabled())
	d.ConfigureTLS(rs.Spec.GetTLSConfig())
	return d
}

func createStatefulHelperFromReplicaSet(sh *mdbv1.MongoDB) *StatefulSetHelper {
	return defaultSetHelper().
		SetName(sh.Name).
		SetService(sh.ServiceName()).
		SetReplicas(sh.Spec.Members).
		SetSecurity(sh.Spec.Security)
}
