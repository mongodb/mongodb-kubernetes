package operator

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReplicaSetBuilder struct {
	*mdbv1.MongoDB
}

func TestReplicaSetEventMethodsHandlePanic(t *testing.T) {
	// restoring
	defer InitDefaultEnvVariables()

	// nullifying env variable will result in panic exception raised
	_ = os.Setenv(util.AutomationAgentImage, "")
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)
	checkReconcileFailed(
		t,
		reconciler,
		rs,
		true,
		"Failed to reconcile Mongodb Replica Set: MONGODB_ENTERPRISE_DATABASE_IMAGE environment variable is not set!",
		client)

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
	_ = client.Get(context.TODO(), objectKeyFromApiObject(rs), set)

	// Now scale up to 5 nodes
	rs = DefaultReplicaSetBuilder().SetMembers(5).Build()
	_ = client.Update(context.TODO(), rs)

	checkReconcileSuccessful(t, reconciler, rs, client)

	updatedSet := &appsv1.StatefulSet{}
	_ = client.Get(context.TODO(), objectKeyFromApiObject(rs), updatedSet)

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

	checkReconcilePending(t, reconciler, rs, "Not all certificates have been approved by Kubernetes CA for temple", client)
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
		reflect.ValueOf(omConn.ReadBackupConfigs), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

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
	approveAgentCSRs(client)

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
	statefulSet := getStatefulSet(client, objectKeyFromApiObject(rs))

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
		context.Version = "4.2.2"
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

func TestOnlyTagIsAppliedToOlderOpsManager(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	reconciler, client := defaultReplicaSetReconciler(rs)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = "4.2.1"
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

// defaultReplicaSetReconciler is the replica set reconciler used in unit test. It "adds" necessary
// additional K8s objects (rs, connection config map and secrets) necessary for reconciliation
// so it's possible to call 'reconcile()' on it right away
func defaultReplicaSetReconciler(rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, *mock.MockedClient) {
	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()

	return newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection), manager.Client
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

func (b *ReplicaSetBuilder) EnableAuth() *ReplicaSetBuilder {
	b.Spec.Security.Authentication.Enabled = true
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
	hostnames, _ := util.GetDnsForStatefulSet(sts, rs.Spec.GetClusterDomain())
	d.MergeReplicaSet(
		buildReplicaSetFromStatefulSet(sts, rs),
		nil,
	)
	d.AddMonitoringAndBackup(hostnames[0], zap.S())
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
