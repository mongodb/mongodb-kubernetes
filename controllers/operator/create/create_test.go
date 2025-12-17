package create

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
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
	assert.Equal(t, "MongoDB", svc.OwnerReferences[0].Kind)
	assert.Equal(t, mock.TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.Equal(t, "label", svc.Labels[appLabelKey])
	assert.NotContains(t, svc.Labels, appsv1.StatefulSetPodNameLabel)
	assert.True(t, svc.Spec.PublishNotReadyAddresses)

	// test podName label not nil
	svc = BuildService(kube.ObjectKey(mock.TestNamespace, "my-svc"), mdb, nil, ptr.To("podName"), 2000, omv1.MongoDBOpsManagerServiceDefinition{
		Type:           corev1.ServiceTypeClusterIP,
		Port:           2000,
		LoadBalancerIP: "loadbalancerip",
	})

	assert.Len(t, svc.OwnerReferences, 1)
	assert.Equal(t, mdb.Name, svc.OwnerReferences[0].Name)
	assert.Equal(t, "MongoDB", svc.OwnerReferences[0].Kind)
	assert.Equal(t, mock.TestNamespace, svc.Namespace)
	assert.Equal(t, "my-svc", svc.Name)
	assert.Equal(t, "loadbalancerip", svc.Spec.LoadBalancerIP)
	assert.Equal(t, "None", svc.Spec.ClusterIP)
	assert.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
	assert.NotContains(t, svc.Labels, appLabelKey)
	assert.Equal(t, "podName", svc.Labels[appsv1.StatefulSetPodNameLabel])
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

	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
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
		SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBPassword("my-secret", "password").SetBackup(omv1.MongoDBOpsManagerBackup{
		Enabled: true,
	}).AddConfiguration("brs.queryable.proxyPort", "1234").
		Build()

	fakeClient, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  fakeClient,
	}

	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
	assert.NoError(t, err)

	svc, err := fakeClient.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.SvcName()))
	assert.NoError(t, err, "Internal service exists")

	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP, "Default internal service for OM multicluster is of type ClusterIP")
	assert.Equal(t, svc.Spec.ClusterIP, "", "Default internal service for OM multicluster is not a headless service")
}

func TestOpsManagerInKubernetes_ClusterSpecificExternalConnectivity(t *testing.T) {
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"

	type testCase struct {
		clusterSpecList            []omv1.ClusterSpecOMItem
		commonExternalConnectivity *omv1.MongoDBOpsManagerServiceDefinition
		expectedServices           map[string]corev1.Service
	}

	testCases := map[string]testCase{
		"no common external connectivity + cluster specific": {
			clusterSpecList: []omv1.ClusterSpecOMItem{
				{
					ClusterName: memberClusterName1,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type: corev1.ServiceTypeNodePort,
						Port: 30006,
					},
				},
				{
					ClusterName: memberClusterName2,
					Members:     1,
				},
				{
					ClusterName: memberClusterName3,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type:           corev1.ServiceTypeLoadBalancer,
						Port:           8080,
						LoadBalancerIP: "10.10.10.1",
					},
				},
			},
			commonExternalConnectivity: nil,
			expectedServices: map[string]corev1.Service{
				memberClusterName1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "mongodb.com/v1",
								Kind:               "MongoDBOpsManager",
								Name:               "test-om",
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       30006,
								TargetPort: intstr.FromInt32(8080),
								NodePort:   30006,
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeNodePort,
						PublishNotReadyAddresses: true,
					},
				},
				memberClusterName3: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "mongodb.com/v1",
								Kind:               "MongoDBOpsManager",
								Name:               "test-om",
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       8080,
								TargetPort: intstr.FromInt32(8080),
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeLoadBalancer,
						LoadBalancerIP:           "10.10.10.1",
						PublishNotReadyAddresses: true,
					},
				},
			},
		},
		"common external connectivity + cluster specific": {
			clusterSpecList: []omv1.ClusterSpecOMItem{
				{
					ClusterName: memberClusterName1,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type: corev1.ServiceTypeNodePort,
						Port: 30006,
					},
				},
				{
					ClusterName: memberClusterName2,
					Members:     1,
				},
				{
					ClusterName: memberClusterName3,
					Members:     1,
					MongoDBOpsManagerExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
						Type:           corev1.ServiceTypeLoadBalancer,
						Port:           8080,
						LoadBalancerIP: "10.10.10.1",
					},
				},
			},
			commonExternalConnectivity: &omv1.MongoDBOpsManagerServiceDefinition{
				Type:           corev1.ServiceTypeLoadBalancer,
				Port:           5005,
				LoadBalancerIP: "20.20.20.2",
				Annotations: map[string]string{
					"test-annotation": "test-value",
				},
			},
			expectedServices: map[string]corev1.Service{
				memberClusterName1: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "mongodb.com/v1",
								Kind:               "MongoDBOpsManager",
								Name:               "test-om",
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       30006,
								TargetPort: intstr.FromInt32(8080),
								NodePort:   30006,
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeNodePort,
						PublishNotReadyAddresses: true,
					},
				},
				memberClusterName2: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "mongodb.com/v1",
								Kind:               "MongoDBOpsManager",
								Name:               "test-om",
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
						Annotations: map[string]string{
							"test-annotation": "test-value",
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       5005,
								TargetPort: intstr.FromInt32(8080),
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeLoadBalancer,
						LoadBalancerIP:           "20.20.20.2",
						PublishNotReadyAddresses: true,
					},
				},
				memberClusterName3: {
					ObjectMeta: metav1.ObjectMeta{
						Name:            "test-om-svc-ext",
						ResourceVersion: "1",
						Labels: map[string]string{
							"app":                   "test-om-svc",
							util.OperatorLabelName:  util.OperatorLabelValue,
							omv1.LabelResourceOwner: "test-om",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "mongodb.com/v1",
								Kind:               "MongoDBOpsManager",
								Name:               "test-om",
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Name:       "external-connectivity-port",
								Port:       8080,
								TargetPort: intstr.FromInt32(8080),
							},
							{
								Name:       "backup-port",
								Port:       1234,
								TargetPort: intstr.FromInt32(0),
								NodePort:   0,
							},
						},
						Selector: map[string]string{
							"app":                  "test-om-svc",
							util.OperatorLabelName: util.OperatorLabelValue,
						},
						Type:                     corev1.ServiceTypeLoadBalancer,
						LoadBalancerIP:           "10.10.10.1",
						PublishNotReadyAddresses: true,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			testOmBuilder := omv1.NewOpsManagerBuilderDefault().
				SetName("test-om").
				SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
				SetAppDBPassword("my-secret", "password").
				SetBackup(omv1.MongoDBOpsManagerBackup{Enabled: true}).
				AddConfiguration("brs.queryable.proxyPort", "1234").
				SetOpsManagerClusterSpecList(tc.clusterSpecList)

			if tc.commonExternalConnectivity != nil {
				testOmBuilder.SetExternalConnectivity(*tc.commonExternalConnectivity)
			}
			testOm := testOmBuilder.Build()

			memberClusters := make([]multicluster.MemberCluster, len(tc.clusterSpecList))
			for clusterIndex, clusterSpecItem := range tc.clusterSpecList {
				fakeClient, _ := mock.NewDefaultFakeClient()
				secretsClient := secrets.SecretClient{
					VaultClient: &vault.VaultClient{},
					KubeClient:  fakeClient,
				}

				memberClusters[clusterIndex] = multicluster.MemberCluster{
					Name:         clusterSpecItem.ClusterName,
					Index:        clusterIndex,
					Replicas:     clusterSpecItem.Members,
					Client:       fakeClient,
					SecretClient: secretsClient,
					Active:       true,
					Healthy:      true,
					Legacy:       false,
				}
			}

			for _, memberCluster := range memberClusters {
				ctx := context.Background()
				sts, err := construct.OpsManagerStatefulSet(ctx, memberCluster.SecretClient, testOm, memberCluster, zap.S())
				assert.NoError(t, err)

				err = OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
				assert.NoError(t, err)

				expectedService, ok := tc.expectedServices[memberCluster.Name]
				svc, err := memberCluster.Client.GetService(ctx, kube.ObjectKey(testOm.Namespace, testOm.ExternalSvcName()))
				if ok {
					assert.NoError(t, err)
					assert.Equal(t, expectedService, svc, "service for cluster %s does not match", memberCluster.Name)
				} else {
					assert.Error(t, err)
				}
			}
		})
	}
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

	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
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
	memberCluster := multicluster.GetLegacyCentralMemberCluster(testOm.Spec.Replicas, 0, fakeClient, secretsClient)
	sts, err := construct.OpsManagerStatefulSet(ctx, secretsClient, testOm, memberCluster, zap.S())
	assert.NoError(t, err)

	err = OpsManagerInKubernetes(ctx, memberCluster, testOm, sts, zap.S())
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
			SpecWrapper: &common.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
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
			SpecWrapper: &common.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
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
			SpecWrapper: &common.ServiceSpecWrapper{Spec: corev1.ServiceSpec{
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

	createShardSts(ctx, t, mdb, log, fakeClient)

	createMongosSts(ctx, t, mdb, log, fakeClient)

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

func createShardSpecAndDefaultCluster(client kubernetesClient.Client, sc *mdbv1.MongoDB) (*mdbv1.ShardedClusterComponentSpec, multicluster.MemberCluster) {
	shardSpec := sc.Spec.ShardSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
			PodSpec:     sc.Spec.PodSpec,
		},
	}

	return shardSpec, multicluster.GetLegacyCentralMemberCluster(sc.Spec.MongodsPerShardCount, 0, client, secrets.SecretClient{KubeClient: client})
}

func createMongosSpec(sc *mdbv1.MongoDB) *mdbv1.ShardedClusterComponentSpec {
	shardSpec := sc.Spec.ConfigSrvSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec
}

func createShardSts(ctx context.Context, t *testing.T, mdb *mdbv1.MongoDB, log *zap.SugaredLogger, kubeClient kubernetesClient.Client) {
	shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, mdb)
	sts := construct.DatabaseStatefulSet(*mdb, construct.ShardOptions(1, shardSpec, memberCluster.Name, construct.GetPodEnvOptions()), log)
	err := DatabaseInKubernetes(ctx, kubeClient, *mdb, sts, construct.ShardOptions(1, shardSpec, memberCluster.Name), log)
	assert.NoError(t, err)
}

func createMongosSts(ctx context.Context, t *testing.T, mdb *mdbv1.MongoDB, log *zap.SugaredLogger, kubeClient kubernetesClient.Client) {
	mongosSpec := createMongosSpec(mdb)
	sts := construct.DatabaseStatefulSet(*mdb, construct.MongosOptions(mongosSpec, multicluster.LegacyCentralClusterName, construct.GetPodEnvOptions()), log)
	err := DatabaseInKubernetes(ctx, kubeClient, *mdb, sts, construct.MongosOptions(mongosSpec, multicluster.LegacyCentralClusterName), log)
	assert.NoError(t, err)
}

func TestResizePVCsStorage(t *testing.T) {
	fakeClient, _ := mock.NewDefaultFakeClient()

	initialSts := createStatefulSet("test", "mongodb-test", "20Gi", "20Gi", "20Gi")

	// Create the StatefulSet that we want to resize the PVC to
	err := fakeClient.CreateStatefulSet(context.TODO(), *initialSts)
	assert.NoError(t, err)

	for _, template := range initialSts.Spec.VolumeClaimTemplates {
		for i := range *initialSts.Spec.Replicas {
			pvc := createPVCFromTemplate(template, initialSts.Name, initialSts.Namespace, i)
			err = fakeClient.Create(context.TODO(), pvc)
			assert.NoError(t, err)
		}
	}

	// PVCs from different STS (same name, but different namespace) should be ignored and not resized
	// https://jira.mongodb.org/browse/HELP-85556
	otherSTS := createStatefulSet("test", "mongodb-test-2", "25Gi", "20Gi", "15Gi")

	// Create the StatefulSet that we want to resize the PVC to
	err = fakeClient.CreateStatefulSet(context.TODO(), *otherSTS)
	assert.NoError(t, err)

	for _, template := range otherSTS.Spec.VolumeClaimTemplates {
		for i := range *otherSTS.Spec.Replicas {
			pvc := createPVCFromTemplate(template, otherSTS.Name, otherSTS.Namespace, i)
			err = fakeClient.Create(context.TODO(), pvc)
			assert.NoError(t, err)
		}
	}

	err = resizePVCsStorage(fakeClient, createStatefulSet("test", "mongodb-test", "30Gi", "30Gi", "20Gi"), zap.S())
	assert.NoError(t, err)

	pvcList := corev1.PersistentVolumeClaimList{}
	err = fakeClient.List(context.TODO(), &pvcList)
	assert.NoError(t, err)

	pvcSizesPerNamespace := map[string]map[string]string{
		"mongodb-test": {
			"data":    "30Gi",
			"journal": "30Gi",
			"logs":    "20Gi",
		},
		"mongodb-test-2": {
			"data":    "25Gi",
			"journal": "20Gi",
			"logs":    "15Gi",
		},
	}

	for _, pvc := range pvcList.Items {
		pvcSizes, ok := pvcSizesPerNamespace[pvc.Namespace]
		if !ok {
			t.Fatalf("unexpected namespace %s for pvc %s", pvc.Namespace, pvc.Name)
		}

		if strings.HasPrefix(pvc.Name, "data") {
			assert.Equal(t, pvc.Spec.Resources.Requests.Storage().String(), pvcSizes["data"])
		} else if strings.HasPrefix(pvc.Name, "journal") {
			assert.Equal(t, pvc.Spec.Resources.Requests.Storage().String(), pvcSizes["journal"])
		} else if strings.HasPrefix(pvc.Name, "logs") {
			assert.Equal(t, pvc.Spec.Resources.Requests.Storage().String(), pvcSizes["logs"])
		} else {
			t.Fatal("no pvc was compared while we should have at least detected and compared one")
		}
	}
}

// Helper function to create a StatefulSet
func createStatefulSet(name, namespace, size1, size2, size3 string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(3)),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(size1),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "journal",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(size2),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "logs",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(size3),
							},
						},
					},
				},
			},
		},
	}
}

func createPVCFromTemplate(pvcTemplate corev1.PersistentVolumeClaim, stsName string, namespace string, ordinal int32) *corev1.PersistentVolumeClaim {
	pvcName := fmt.Sprintf("%s-%s-%d", pvcTemplate.Name, stsName, ordinal)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: pvcTemplate.Spec,
	}
}

func TestResourceStorageHasChanged(t *testing.T) {
	type args struct {
		existingPVC []corev1.PersistentVolumeClaim
		toCreatePVC []corev1.PersistentVolumeClaim
	}
	tests := []struct {
		name string
		args args
		want []pvcResize
	}{
		{
			name: "empty",
			want: nil,
		},
		{
			name: "existing is larger",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
							},
						},
					},
				},
				toCreatePVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
			},
			want: []pvcResize{{resizeIndicator: 1, from: "2Gi", to: "1Gi"}},
		},
		{
			name: "toCreate is larger",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
				toCreatePVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
							},
						},
					},
				},
			},
			want: []pvcResize{{resizeIndicator: -1, from: "1Gi", to: "2Gi"}},
		},
		{
			name: "both are equal",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
				toCreatePVC: []corev1.PersistentVolumeClaim{
					{
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
			},
			want: []pvcResize{{resizeIndicator: 0, from: "1Gi", to: "1Gi"}},
		},
		{
			name: "none exist",
			args: args{
				existingPVC: []corev1.PersistentVolumeClaim{},
				toCreatePVC: []corev1.PersistentVolumeClaim{},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, resourceStorageHasChanged(tt.args.existingPVC, tt.args.toCreatePVC), "resourceStorageHasChanged(%v, %v)", tt.args.existingPVC, tt.args.toCreatePVC)
		})
	}
}

func TestHasFinishedResizing(t *testing.T) {
	stsName := "test"
	desiredSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: stsName},
		Spec: appsv1.StatefulSetSpec{
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("20Gi"),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "logs",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("30Gi"),
							},
						},
					},
				},
			},
		},
	}

	ctx := context.TODO()
	{
		fakeClient, _ := mock.NewDefaultFakeClient()
		// Scenario 1: All PVCs have finished resizing
		pvc1 := createPVCWithCapacity("data-"+stsName+"-0", "20Gi")
		pvc2 := createPVCWithCapacity("logs-"+stsName+"-0", "30Gi")
		notPartOfSts := createPVCWithCapacity("random-sts-0", "30Gi")
		err := fakeClient.Create(ctx, pvc1)
		assert.NoError(t, err)
		err = fakeClient.Create(ctx, pvc2)
		assert.NoError(t, err)
		err = fakeClient.Create(ctx, notPartOfSts)
		assert.NoError(t, err)

		finished, err := hasFinishedResizing(ctx, fakeClient, desiredSts)
		assert.NoError(t, err)
		assert.True(t, finished, "PVCs should be finished resizing")
	}

	{
		// Scenario 2: Some PVCs are still resizing
		fakeClient, _ := mock.NewDefaultFakeClient()
		pvc2Incomplete := createPVCWithCapacity("logs-"+stsName+"-0", "10Gi")
		err := fakeClient.Create(ctx, pvc2Incomplete)
		assert.NoError(t, err)

		finished, err := hasFinishedResizing(ctx, fakeClient, desiredSts)
		assert.NoError(t, err)
		assert.False(t, finished, "PVCs should not be finished resizing")
	}
}

// Helper function to create a PVC with a specific capacity and status
func createPVCWithCapacity(name string, capacity string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(capacity),
				},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(capacity),
			},
		},
	}
}

func TestGetMatchingPVCTemplateFromSTS(t *testing.T) {
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example-sts",
		},
		Spec: appsv1.StatefulSetSpec{
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data-pvc",
					},
					Spec: corev1.PersistentVolumeClaimSpec{},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "logs-pvc",
					},
					Spec: corev1.PersistentVolumeClaimSpec{},
				},
			},
		},
	}

	tests := []struct {
		name             string
		pvcName          string
		expectedTemplate *corev1.PersistentVolumeClaim
		expectedIndex    int
	}{
		{
			name:    "Matching data-pvc with ordinal 0",
			pvcName: "data-pvc-example-sts-0",
			expectedTemplate: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "data-pvc",
				},
			},
			expectedIndex: 0,
		},
		{
			name:    "Matching logs-pvc with ordinal 1",
			pvcName: "logs-pvc-example-sts-1",
			expectedTemplate: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "logs-pvc",
				},
			},
			expectedIndex: 1,
		},
		{
			name:             "Non-matching PVC name",
			pvcName:          "cache-pvc-example-sts-0",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:    "Matching data-pvc with high ordinal",
			pvcName: "data-pvc-example-sts-1000",
			expectedTemplate: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "data-pvc",
				},
			},
			expectedIndex: 0,
		},
		{
			name:             "PVC name with similar prefix but different StatefulSet name",
			pvcName:          "data-pvc-other-sts-0",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:             "Not matching logs-pvc without ordinal",
			pvcName:          "logs-pvc-example-sts",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:             "Empty PVC name",
			pvcName:          "",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
		{
			name:             "PVC name with extra suffix",
			pvcName:          "data-pvc-example-sts-extra-0",
			expectedTemplate: nil,
			expectedIndex:    -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.pvcName,
				},
			}

			template, index := getMatchingPVCTemplateFromSTS(statefulSet, p)

			if tt.expectedTemplate == nil {
				assert.Nil(t, template, "Expected no matching PVC template")
			} else {
				if assert.NotNil(t, template, "Expected a matching PVC template") {
					assert.Equal(t, tt.expectedTemplate.Name, template.Name, "PVC template name should match")
				}
			}

			assert.Equal(t, tt.expectedIndex, index, "PVC template index should match")
		})
	}
}

func TestCheckStatefulsetIsDeleted(t *testing.T) {
	ctx := context.TODO()
	sleepDuration := 10 * time.Millisecond
	log := zap.NewNop().Sugar()

	namespace := "default"
	stsName := "test-sts"
	desiredSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: namespace,
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
	}

	t.Run("StatefulSet is deleted", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		err := fakeClient.CreateStatefulSet(ctx, *desiredSts)
		assert.NoError(t, err)

		// Simulate the deletion by deleting the StatefulSet
		err = fakeClient.DeleteStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
		assert.NoError(t, err)

		// Check if the StatefulSet is detected as deleted
		result := checkStatefulsetIsDeleted(ctx, fakeClient, desiredSts, sleepDuration, log)

		assert.True(t, result, "StatefulSet should be detected as deleted")
	})

	t.Run("StatefulSet is not deleted", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		err := fakeClient.CreateStatefulSet(ctx, *desiredSts)
		assert.NoError(t, err)

		// Do not delete the StatefulSet, to simulate it still existing
		// Check if the StatefulSet is detected as not deleted
		result := checkStatefulsetIsDeleted(ctx, fakeClient, desiredSts, sleepDuration, log)

		assert.False(t, result, "StatefulSet should not be detected as deleted")
	})

	t.Run("StatefulSet is deleted after some retries", func(t *testing.T) {
		fakeClient, _ := mock.NewDefaultFakeClient()
		err := fakeClient.CreateStatefulSet(ctx, *desiredSts)
		assert.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		// Use a goroutine to delete the StatefulSet after a delay, making it race-safe
		go func() {
			defer wg.Done()
			time.Sleep(20 * time.Millisecond) // Wait for a bit longer than the first sleep
			err = fakeClient.DeleteStatefulSet(ctx, kube.ObjectKey(desiredSts.Namespace, desiredSts.Name))
			assert.NoError(t, err)
		}()

		// Check if the StatefulSet is detected as deleted after retries
		result := checkStatefulsetIsDeleted(ctx, fakeClient, desiredSts, sleepDuration, log)

		wg.Wait()

		assert.True(t, result, "StatefulSet should be detected as deleted after retries")
	})
}
