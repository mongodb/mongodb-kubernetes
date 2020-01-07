package operator

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/authentication"

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
	// nullifying env variable will result in panic exception raised
	_ = os.Setenv(util.AutomationAgentImageUrl, "")
	rs := DefaultReplicaSetBuilder().Build()

	manager := newMockedManager(rs)
	checkReconcileFailed(
		t,
		newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection),
		rs,
		true,
		"Failed to reconcile Mongodb Replica Set: MONGODB_ENTERPRISE_DATABASE_IMAGE environment variable is not set!",
		manager.client)

	// restoring
	InitDefaultEnvVariables()
}

func TestCreateReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().Build()

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, client)

	assert.Len(t, client.services, 1)
	assert.Len(t, client.sets, 1)
	assert.Equal(t, *client.getSet(rs.ObjectKey()).Spec.Replicas, int32(3))
	assert.Len(t, client.secrets, 2)

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromReplicaSet(rs), "auth", "ssl")
	connection.CheckNumberOfUpdateRequests(t, 1)
}

func TestHorizonVerificationTLS(t *testing.T) {
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		mdbv1.MongoDBHorizonConfig{"my-horizon": "my-db.com:12345"},
		mdbv1.MongoDBHorizonConfig{"my-horizon": "my-db.com:12346"},
		mdbv1.MongoDBHorizonConfig{"my-horizon": "my-db.com:12347"},
	}
	rs := DefaultReplicaSetBuilder().SetReplicaSetHorizons(replicaSetHorizons).Build()

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	msg := "TLS must be enabled in order to set replica set horizons"
	checkReconcileFailed(t, reconciler, rs, false, msg, client)
}

func TestHorizonVerificationCount(t *testing.T) {
	replicaSetHorizons := []mdbv1.MongoDBHorizonConfig{
		mdbv1.MongoDBHorizonConfig{"my-horizon": "my-db.com:12345"},
		mdbv1.MongoDBHorizonConfig{"my-horizon": "my-db.com:12346"},
	}
	rs := DefaultReplicaSetBuilder().
		EnableTLS().
		SetReplicaSetHorizons(replicaSetHorizons).
		Build()

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	msg := "Number of horizons must be equal to number of members in replica set"
	checkReconcileFailed(t, reconciler, rs, false, msg, client)
}

// TestScaleUpReplicaSet verifies scaling up for replica set. Statefulset and OM Deployment must be changed accordingly
func TestScaleUpReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetMembers(3).Build()

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

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

	manager := newMockedManager(rs)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

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
	st := DefaultReplicaSetBuilder().Build()

	kubeManager := newMockedManager(st)
	reconciler := newReplicaSetReconciler(kubeManager, om.NewEmptyMockedOmConnectionWithDelay)

	checkReconcileSuccessful(t, reconciler, st, kubeManager.client)
	omConn := om.CurrMockedConnection
	omConn.CleanHistory()

	// Now delete it
	assert.NoError(t, reconciler.delete(st, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.ReadBackupConfigs), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

func TestX509IsNotEnabledWithOlderVersionsOfOpsManager(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableAuth().EnableTLS().SetAuthModes([]string{util.X509}).Build()
	kubeManager := newMockedManager(rs)

	addKubernetesTlsResources(kubeManager.client, rs)
	approveAgentCSRs(kubeManager.client)

	reconciler := newReplicaSetReconciler(kubeManager, func(context *om.OMContext) om.Connection {
		conn := om.NewEmptyMockedOmConnection(context)

		// make the mocked connection return an error behaving as an older version of Ops Manager
		conn.(*om.MockedOmConnection).UpdateMonitoringAgentConfigFunc = func(mac *om.MonitoringAgentConfig, log *zap.SugaredLogger) (bytes []byte, e error) {
			return nil, fmt.Errorf("some error. Detail: %s", util.MethodNotAllowed)
		}
		return conn
	})

	checkReconcileFailed(t, reconciler, rs, true, "unable to configure X509 with this version of Ops Manager", kubeManager.client)
}

func TestReplicaSetScramUpgradeDowngrade(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetVersion("4.0.0").EnableAuth().SetAuthModes([]string{"SCRAM"}).Build()

	kubeManager := newMockedManager(rs)
	reconciler := newReplicaSetReconciler(kubeManager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, kubeManager.client)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Contains(t, ac.Auth.AutoAuthMechanisms, string(authentication.ScramSha256))

	// downgrade to version that will not use SCRAM-SHA-256
	rs.Spec.Version = "3.6.9"

	client := kubeManager.client
	_ = client.Update(context.TODO(), rs)

	checkReconcileFailed(t, reconciler, rs, true, "Unable to downgrade to SCRAM-SHA-1 when SCRAM-SHA-256 has been enabled", kubeManager.client)
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

	kubeManager := newMockedManager(rs)

	addKubernetesTlsResources(kubeManager.client, rs)

	reconciler := newReplicaSetReconciler(kubeManager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, kubeManager.client)

	// read the stateful set that was created by the operator
	assertPodSpecSts(t, getStatefulSet(kubeManager.client, objectKeyFromApiObject(rs)))
}

func DefaultReplicaSetBuilder() *ReplicaSetBuilder {
	podSpec := NewDefaultPodSpec()
	spec := mdbv1.MongoDbSpec{
		Version:    "4.0.0",
		Persistent: util.BooleanRef(false),
		ConnectionSpec: mdbv1.ConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{
					Name: TestProjectConfigMapName,
				},
			},
			Credentials: TestCredentialsSecretName,
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
	rs := &mdbv1.MongoDB{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: TestNamespace}}
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
	return b.MongoDB
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
