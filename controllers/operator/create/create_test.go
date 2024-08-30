package create

import (
	"context"
	"fmt"
	"testing"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"k8s.io/utils/ptr"

	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
	svc := BuildService(kube.ObjectKey(mock.TestNamespace, "my-svc"), mdb, ptr.To("label"), nil, 2000, omv1.MongoDBOpsManagerServiceDefinition{
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
	assert.Equal(t, "label", svc.Labels[appLabelKey])
	assert.NotContains(t, svc.Labels, podNameLabelKey)
	assert.True(t, svc.Spec.PublishNotReadyAddresses)

	// test podName label not nil
	svc = BuildService(kube.ObjectKey(mock.TestNamespace, "my-svc"), mdb, nil, ptr.To("podName"), 2000, omv1.MongoDBOpsManagerServiceDefinition{
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
	assert.NotContains(t, svc.Labels, appLabelKey)
	assert.Equal(t, "podName", svc.Labels[podNameLabelKey])
	assert.True(t, svc.Spec.PublishNotReadyAddresses)
}

func TestOpsManagerInKubernetes_InternalConnectivityOverride(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetInternalConnectivity(omv1.MongoDBOpsManagerServiceDefinition{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: ptr.To("0.0.12.0"),
			Port:      5000,
		}).
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient), zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, fakeClient, testOm, sts, zap.S())
	assert.NoError(t, err)

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "The operator creates a ClusterIP service if explicitly requested to do so.")
	assert.Equal(t, svc.Spec.ClusterIP, "0.0.12.0", "The operator configures the requested ClusterIP for the service")

	assert.Len(t, svc.Spec.Ports, 2, "Backup port should have been added to existing internal service")

	port0 := svc.Spec.Ports[0]
	assert.Equal(t, internalConnectivityPortName, port0.Name)

	port1 := svc.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}

func TestOpsManagerInKubernetes_DefaultInternalServiceForMultiCluster(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetOpsManagerTopology(omv1.ClusterTopologyMultiCluster).
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient), zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, fakeClient, testOm, sts, zap.S())
	assert.NoError(t, err)

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "Default internal service for OM multicluster is of type ClusterIP")
	assert.Equal(t, svc.Spec.ClusterIP, "", "Default internal service for OM multicluster is not a headless service")
}

func TestBackupServiceCreated_NoExternalConnectivity(t *testing.T) {
	ctx := context.Background()
	testOm := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient), zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, fakeClient, testOm, sts, zap.S())
	assert.NoError(t, err)

	_, err = fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()+"-ext"))
	assert.Error(t, err, "No external service should have been created")

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "Default internal service is of type ClusterIP")
	assert.Equal(t, svc.Spec.ClusterIP, corev1.ClusterIPNone, "Default internal service is a headless service")

	assert.Len(t, svc.Spec.Ports, 2, "Backup port should have been added to existing internal service")

	port0 := svc.Spec.Ports[0]
	assert.Equal(t, internalConnectivityPortName, port0.Name)

	port1 := svc.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}

func TestBackupServiceCreated_ExternalConnectivity(t *testing.T) {
	ctx := context.Background()
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
	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient), zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, fakeClient, testOm, sts, zap.S())
	assert.NoError(t, err)

	externalService, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()+"-ext"))
	assert.NoError(t, err, "An External service should have been created")

	assert.Len(t, externalService.Spec.Ports, 2, "Backup port should have been added to existing external service")

	port0 := externalService.Spec.Ports[0]
	assert.Equal(t, externalConnectivityPortName, port0.Name)
	assert.Equal(t, int32(5000), port0.Port)
	assert.Equal(t, intstr.FromInt(8080), port0.TargetPort)
	assert.Equal(t, int32(5000), port0.NodePort)

	port1 := externalService.Spec.Ports[1]
	assert.Equal(t, backupPortName, port1.Name)
	assert.Equal(t, int32(1234), port1.Port)
}

func TestDatabaseInKubernetes_ExternalServicesWithoutExternalDomain(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
			},
			Type:                     corev1.ServiceTypeLoadBalancer,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	expectedServices := []corev1.Service{service1, service2}

	testDatabaseInKubernetesExternalServices(ctx, t, mdbv1.ExternalAccessConfiguration{}, expectedServices)
}

func TestDatabaseInKubernetes_ExternalServicesWithExternalDomainHaveAdditionalBackupPort(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
				{
					Name:       "backup",
					TargetPort: intstr.IntOrString{IntVal: 27018},
				},
			},
			Type:                     corev1.ServiceTypeLoadBalancer,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	expectedServices := []corev1.Service{service1, service2}

	testDatabaseInKubernetesExternalServices(ctx, t, mdbv1.ExternalAccessConfiguration{ExternalDomain: ptr.To("example.com")}, expectedServices)
}

func TestDatabaseInKubernetes_ExternalServicesWithServiceSpecOverrides(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external", Annotations: map[string]string{
			"key": "value",
		}},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
				{
					Name:       "backup",
					TargetPort: intstr.IntOrString{IntVal: 27018},
				},
			},
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	expectedServices := []corev1.Service{service1, service2}

	externalAccessConfiguration := mdbv1.ExternalAccessConfiguration{
		ExternalDomain: ptr.To("example.com"),
		ExternalService: mdbv1.ExternalServiceConfiguration{
			SpecWrapper: &mdbv1.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			}},
			Annotations: map[string]string{
				"key": "value",
			},
		},
	}
	testDatabaseInKubernetesExternalServices(ctx, t, externalAccessConfiguration, expectedServices)
}

const (
	defaultResourceName = "mdb"
	defaultNamespace    = "my-namespace"
)

func TestDatabaseInKubernetes_ExternalServicesWithPlaceholders(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external", Annotations: map[string]string{
			"key": "value",
		}},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
			},
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	externalAccessConfiguration := mdbv1.ExternalAccessConfiguration{
		ExternalService: mdbv1.ExternalServiceConfiguration{
			SpecWrapper: &mdbv1.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			}},
			Annotations: map[string]string{
				PlaceholderPodIndex:            "{podIndex}",
				PlaceholderNamespace:           "{namespace}",
				PlaceholderResourceName:        "{resourceName}",
				PlaceholderPodName:             "{podName}",
				PlaceholderStatefulSetName:     "{statefulSetName}",
				PlaceholderExternalServiceName: "{externalServiceName}",
				PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
				PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
			},
		},
	}

	podIndex := 0
	podName := fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	mongodProcessDomain := fmt.Sprintf("%s-svc.%s.svc.cluster.local", defaultResourceName, defaultNamespace)
	service1.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	podIndex = 1
	podName = fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	service2.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	testDatabaseInKubernetesExternalServices(ctx, t, externalAccessConfiguration, []corev1.Service{service1, service2})
}

func TestDatabaseInKubernetes_ExternalServicesWithPlaceholders_WithExternalDomain(t *testing.T) {
	ctx := context.Background()
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-0-svc-external", Annotations: map[string]string{
			"key": "value",
		}},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "mongodb",
					TargetPort: intstr.IntOrString{IntVal: 27017},
				},
				{
					Name:       "backup",
					TargetPort: intstr.IntOrString{IntVal: 27018},
				},
			},
			Type:                     corev1.ServiceTypeNodePort,
			PublishNotReadyAddresses: true,
		},
	}

	service1 := svc
	service1.Name = "mdb-0-svc-external"
	service2 := svc
	service2.Name = "mdb-1-svc-external"
	externalDomain := "external.domain.example.com"
	externalAccessConfiguration := mdbv1.ExternalAccessConfiguration{
		ExternalDomain: &externalDomain,
		ExternalService: mdbv1.ExternalServiceConfiguration{
			SpecWrapper: &mdbv1.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
			}},
			Annotations: map[string]string{
				PlaceholderPodIndex:            "{podIndex}",
				PlaceholderNamespace:           "{namespace}",
				PlaceholderResourceName:        "{resourceName}",
				PlaceholderPodName:             "{podName}",
				PlaceholderStatefulSetName:     "{statefulSetName}",
				PlaceholderExternalServiceName: "{externalServiceName}",
				PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
				PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
			},
		},
	}

	podIndex := 0
	podName := fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	mongodProcessDomain := externalDomain
	service1.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	podIndex = 1
	podName = fmt.Sprintf("%s-%d", defaultResourceName, podIndex)
	service2.Annotations = map[string]string{
		PlaceholderPodIndex:            fmt.Sprintf("%d", podIndex),
		PlaceholderNamespace:           defaultNamespace,
		PlaceholderResourceName:        defaultResourceName,
		PlaceholderPodName:             podName,
		PlaceholderStatefulSetName:     defaultResourceName,
		PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
		PlaceholderMongodProcessDomain: mongodProcessDomain,
		PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s.%s", podName, mongodProcessDomain),
	}

	testDatabaseInKubernetesExternalServices(ctx, t, externalAccessConfiguration, []corev1.Service{service1, service2})
}

func testDatabaseInKubernetesExternalServices(ctx context.Context, t *testing.T, externalAccessConfiguration mdbv1.ExternalAccessConfiguration, expectedServices []corev1.Service) {
	log := zap.S()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName(defaultResourceName).
		SetNamespace(defaultNamespace).
		SetMembers(2).
		Build()
	mdb.Spec.ExternalAccessConfiguration = &externalAccessConfiguration

	sts := construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions(construct.GetPodEnvOptions()), log)
	err := DatabaseInKubernetes(ctx, fakeClient, *mdb, sts, construct.ReplicaSetOptions(), log)
	assert.NoError(t, err)

	// we only test a subset of fields from service spec, which are the most relevant for external services
	for _, expectedService := range expectedServices {
		actualService, err := fakeClient.GetService(ctx, types.NamespacedName{Name: expectedService.GetName(), Namespace: defaultNamespace})
		require.NoError(t, err, "serviceName: %s", expectedService.GetName())
		require.NotNil(t, actualService)
		require.Len(t, actualService.Spec.Ports, len(expectedService.Spec.Ports))
		for i, expectedPort := range expectedService.Spec.Ports {
			actualPort := actualService.Spec.Ports[i]
			assert.Equal(t, expectedPort.Name, actualPort.Name)
			assert.Equal(t, expectedPort.TargetPort.IntVal, actualPort.TargetPort.IntVal)
		}
		assert.Equal(t, expectedService.Spec.Type, actualService.Spec.Type)
		assert.True(t, expectedService.Spec.PublishNotReadyAddresses, actualService.Spec.PublishNotReadyAddresses)
		if expectedService.Annotations != nil {
			assert.Equal(t, expectedService.Annotations, actualService.Annotations)
		}
	}

	// disable external access -> remove external services
	mdb.Spec.ExternalAccessConfiguration = nil
	err = DatabaseInKubernetes(ctx, fakeClient, *mdb, sts, construct.ReplicaSetOptions(), log)
	assert.NoError(t, err)

	for _, expectedService := range expectedServices {
		_, err := fakeClient.GetService(ctx, types.NamespacedName{Name: expectedService.GetName(), Namespace: defaultNamespace})
		assert.True(t, errors.IsNotFound(err))
	}
}

func TestDatabaseInKubernetesExternalServicesSharded(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	fakeClient, _ := mock.NewDefaultFakeClient()
	mdb := mdbv1.NewDefaultShardedClusterBuilder().
		SetName("mdb").
		SetNamespace("my-namespace").
		SetMongosCountSpec(2).
		SetShardCountSpec(1).
		SetConfigServerCountSpec(1).
		Build()

	mdb.Spec.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{}

	err := createShardSts(ctx, t, mdb, log, fakeClient)
	require.NoError(t, err)

	err = createMongosSts(ctx, t, mdb, log, fakeClient)
	require.NoError(t, err)

	actualService, err := fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-mongos-0-svc-external", Namespace: "my-namespace"})
	require.NoError(t, err)
	require.NotNil(t, actualService)

	actualService, err = fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-mongos-1-svc-external", Namespace: "my-namespace"})
	require.NoError(t, err)
	require.NotNil(t, actualService)

	_, err = fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-config-0-svc-external", Namespace: "my-namespace"})
	require.Errorf(t, err, "expected no config service")

	_, err = fakeClient.GetService(ctx, types.NamespacedName{Name: "mdb-0-svc-external", Namespace: "my-namespace"})
	require.Errorf(t, err, "expected no shard service")
}

func createShardSts(ctx context.Context, t *testing.T, mdb *mdbv1.MongoDB, log *zap.SugaredLogger, kubeClient kubernetesClient.Client) error {
	sts := construct.DatabaseStatefulSet(*mdb, construct.ShardOptions(1, construct.GetPodEnvOptions()), log)
	err := DatabaseInKubernetes(ctx, kubeClient, *mdb, sts, construct.ShardOptions(1), log)
	assert.NoError(t, err)
	return err
}

func createMongosSts(ctx context.Context, t *testing.T, mdb *mdbv1.MongoDB, log *zap.SugaredLogger, kubeClient kubernetesClient.Client) error {
	sts := construct.DatabaseStatefulSet(*mdb, construct.MongosOptions(construct.GetPodEnvOptions()), log)
	err := DatabaseInKubernetes(ctx, kubeClient, *mdb, sts, construct.MongosOptions(), log)
	assert.NoError(t, err)
	return err
}
