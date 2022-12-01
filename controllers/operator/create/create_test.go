package create

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"go.uber.org/zap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func init() {
	mock.InitDefaultEnvVariables()
}

func TestBuildService(t *testing.T) {
	mdb := mdbv1.NewReplicaSetBuilder().Build()
	svc := buildService(kube.ObjectKey(mock.TestNamespace, "my-svc"), mdb, "label", 2000, omv1.MongoDBOpsManagerServiceDefinition{
		Type:           corev1.ServiceTypeClusterIP,
		Port:           2000,
		LoadBalancerIP: "loadbalancerip",
	})

	assert.Len(t, svc.OwnerReferences, 1)
	assert.Equal(t, mdb.Name, svc.OwnerReferences[0].Name)
	assert.Equal(t, mdb.GetObjectKind().GroupVersionKind().Kind, svc.OwnerReferences[0].Kind)
	assert.Equal(t, mock.TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.Equal(t, "label", "label")
}

func TestBackupServiceCreated_NoExternalConnectivity(t *testing.T) {
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	client := mock.NewClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := construct.OpsManagerStatefulSet(secretsClient, testOm, zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(client, testOm, sts, zap.S())
	assert.NoError(t, err)

	_, err = client.GetService(kube.ObjectKey(testOm.Namespace, testOm.SvcName()+"-ext"))
	assert.Error(t, err, "No external service should have been created")

	svc, err := client.GetService(kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Len(t, svc.Spec.Ports, 2, "Backup Service should have been added to existing external service")

	port0 := svc.Spec.Ports[0]
	assert.Equal(t, internalConnectivityPortName, port0.Name)

	port1 := svc.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)

}

func TestBackupServiceCreated_ExternalConnectivity(t *testing.T) {
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetAppDBPassword("my-secret", "password").
		SetBackup(omv1.MongoDBOpsManagerBackup{
			Enabled: true,
		}).AddConfiguration("brs.queryable.proxyPort", "1234").
		SetExternalConnectivity(omv1.MongoDBOpsManagerServiceDefinition{
			Type: corev1.ServiceTypeNodePort,
			Port: 5000,
		}).
		Build()
	client := mock.NewClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := construct.OpsManagerStatefulSet(secretsClient, testOm, zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(client, testOm, sts, zap.S())
	assert.NoError(t, err)

	externalService, err := client.GetService(kube.ObjectKey(testOm.Namespace, testOm.SvcName()+"-ext"))
	assert.NoError(t, err, "An External service should have been created")

	assert.Len(t, externalService.Spec.Ports, 2, "Backup Service should have been added to existing external service")

	port0 := externalService.Spec.Ports[0]
	assert.Equal(t, externalConnectivityPortName, port0.Name)
	assert.Equal(t, int32(5000), port0.Port)
	assert.Equal(t, intstr.FromInt(8080), port0.TargetPort)
	assert.Equal(t, int32(5000), port0.NodePort)

	port1 := externalService.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}
