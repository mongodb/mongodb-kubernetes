package operator

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/create"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const opsManagerUserPassword = "MBPYfkAj5ZM0l9uw6C7ggw" //nolint

func TestAppDBManagedCABundleCopyCarriesOwnerUID(t *testing.T) {
	ctx := t.Context()
	manager, central, r := newMultiClusterManagedAppDB(t)
	bundleName := manager.Spec.AppDB.GetCAConfigMapName()
	require.NoError(t, central.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: bundleName, Namespace: manager.Namespace, UID: "central-bundle"},
		Data:       map[string]string{"ca-pem": "issued"},
	}))

	appDB, err := NewAppDBReplicaSetReconciler(ctx, images.ImageUrls{}, "", manager, r.ReconcileCommonController, om.NewOpsManagerConnection, r.memberClustersMap, architectures.NonStatic, "", zap.S())
	require.NoError(t, err)
	require.True(t, appDB.replicateTLSCAConfigMap(ctx, manager, zap.S()).IsOK())

	copied := &corev1.ConfigMap{}
	require.NoError(t, r.memberClustersMap["west"].Get(ctx, kube.ObjectKey(manager.Namespace, bundleName), copied))
	assert.Equal(t, certs.ManagedCertificateOwnerLabels(manager), copied.Labels)
}

func TestAppDBManagedCertificatesMultiClusterDelivery(t *testing.T) {
	ctx := t.Context()
	manager, central, r := newMultiClusterManagedAppDB(t)
	r.memberClustersMap["east"] = central
	returning := r.memberClustersMap["west"]
	delete(r.memberClustersMap, "west")
	preparation := r.ensureAppDBCertAndCA(ctx, manager, zap.S())
	require.NotNil(t, preparation.condition)
	leaf := getManagedCertificateObject(t, ctx, central, "managed-om-db-managed-cert")
	ca, issue := managedTestCAWithDNSNames(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, leaf.Spec.DNSNames...)
	markAllManagedCertificatesReady(t, ctx, central)
	createSecretsForManagedCertificates(t, ctx, central, ca)
	crt, key := issue()
	source, err := central.GetSecret(ctx, kube.ObjectKey(manager.Namespace, leaf.Spec.SecretName))
	require.NoError(t, err)
	source.Type = corev1.SecretTypeTLS
	source.Data = map[string][]byte{"tls.crt": crt, "tls.key": key, "ca.crt": ca}
	require.NoError(t, central.Update(ctx, &source))
	preparation = r.ensureAppDBCertAndCA(ctx, manager, zap.S())
	require.True(t, preparation.status.IsOK(), preparation.status.Message())
	bundleBefore, err := central.GetConfigMap(ctx, kube.ObjectKey(manager.Namespace, "managed-om-db-managed-server-ca-bundle"))
	require.NoError(t, err)
	require.NotEmpty(t, bundleBefore.OwnerReferences)
	bundleBefore.UID = types.UID("managed-appdb-bundle")
	require.NoError(t, central.Update(ctx, &bundleBefore))

	reconciler, err := newAppDbMultiReconciler(ctx, central, manager, r.memberClustersMap, zap.S(), nil)
	require.NoError(t, err)
	status := reconciler.replicateTLSCAConfigMap(ctx, manager, zap.S())
	require.True(t, status.IsOK(), status.Message())
	bundleAfter, err := central.GetConfigMap(ctx, kube.ObjectKey(manager.Namespace, bundleBefore.Name))
	require.NoError(t, err)
	assert.Equal(t, bundleBefore.OwnerReferences, bundleAfter.OwnerReferences, "a central cluster that is also a member must keep the actual OM owner")
	assert.Equal(t, bundleBefore.ResourceVersion, bundleAfter.ResourceVersion, "replication must not rewrite the central source")
	assert.True(t, reconciler.ensureTLSSecretAndCreatePEMIfNeeded(ctx, manager, zap.S()).IsOK())
	pemName := source.Name + "-pem"
	firstPEM, err := central.GetSecret(ctx, kube.ObjectKey(manager.Namespace, pemName))
	require.NoError(t, err)
	firstHash := string(firstPEM.Data[util.LatestHashSecretKey])
	require.NotEmpty(t, firstHash)
	assert.Equal(t, append(append([]byte{}, crt...), key...), firstPEM.Data[firstHash])

	crt, key = issue()
	source, err = central.GetSecret(ctx, kube.ObjectKey(manager.Namespace, leaf.Spec.SecretName))
	require.NoError(t, err)
	source.Data["tls.crt"], source.Data["tls.key"] = crt, key
	require.NoError(t, central.Update(ctx, &source))
	require.True(t, reconciler.ensureTLSSecretAndCreatePEMIfNeeded(ctx, manager, zap.S()).IsOK())
	renewed, err := central.GetSecret(ctx, kube.ObjectKey(manager.Namespace, pemName))
	require.NoError(t, err)
	renewedHash := string(renewed.Data[util.LatestHashSecretKey])
	assert.NotEqual(t, firstHash, renewedHash)
	assert.Equal(t, firstPEM.Data[firstHash], renewed.Data[firstHash])
	assert.Equal(t, firstHash, string(renewed.Data[util.PreviousHashSecretKey]))

	r.memberClustersMap["west"] = returning
	reconciler, err = newAppDbMultiReconciler(ctx, central, manager, r.memberClustersMap, zap.S(), nil)
	require.NoError(t, err)
	status = reconciler.replicateTLSCAConfigMap(ctx, manager, zap.S())
	require.True(t, status.IsOK(), status.Message())
	status = reconciler.ensureTLSSecretAndCreatePEMIfNeeded(ctx, manager, zap.S())
	require.True(t, status.IsOK(), status.Message())
	remoteBundle := &corev1.ConfigMap{}
	require.NoError(t, returning.Get(ctx, kube.ObjectKey(manager.Namespace, bundleBefore.Name), remoteBundle))
	assert.Equal(t, bundleBefore.Data, remoteBundle.Data)
	assert.Equal(t, certs.ManagedCertificateOwnerLabels(manager), remoteBundle.Labels, "remote managed trust must be discoverable for cleanup")
	assert.Empty(t, remoteBundle.OwnerReferences, "central ownership must not be copied to another cluster")
	remotePEM := &corev1.Secret{}
	require.NoError(t, returning.Get(ctx, kube.ObjectKey(manager.Namespace, pemName), remotePEM))
	assert.Equal(t, certs.ManagedCertificateOwnerLabels(certs.AppDBManagedCertificateOwner(manager)), remotePEM.Labels,
		"a member-cluster AppDB -pem copy has to carry the UID label the delete sweep matches")
	assert.Empty(t, remotePEM.OwnerReferences, "a reference to an owner in another cluster makes Kubernetes delete the secret")
	assert.Equal(t, renewedHash, string(remotePEM.Data[util.LatestHashSecretKey]), "returning clusters receive the renewed leaf")
	assert.Equal(t, renewed.Data[renewedHash], remotePEM.Data[renewedHash])
	assert.True(t, apiErrors.IsNotFound(returning.Get(ctx, kube.ObjectKey(manager.Namespace, source.Name), &corev1.Secret{})), "raw issued TLS secrets stay central")

	member := reconciler.helper.getMemberCluster("west")
	scaler := scalers.GetAppDBScaler(manager, member.Name, member.Index, reconciler.helper.memberClusters)
	sts, err := construct.AppDbStatefulSet(*manager, nil, construct.AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, architectures.NonStatic, zap.S())
	require.NoError(t, err)
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		switch volume.Name {
		case "secret-ca":
			require.NotNil(t, volume.ConfigMap)
			assert.Equal(t, "managed-om-db-managed-server-ca-bundle", volume.ConfigMap.Name)
		case util.SecretVolumeName:
			require.NotNil(t, volume.Secret)
			assert.Equal(t, "managed-om-db-managed-cert-pem", volume.Secret.SecretName)
		}
	}
	require.NoError(t, createOpsManagerUserPasswordSecret(ctx, central, manager, opsManagerUserPassword))
	ac, err := reconciler.buildAppDbAutomationConfig(ctx, manager, nil, "", "west", zap.S())
	require.NoError(t, err)
	require.NotEmpty(t, ac.Processes)
	for _, process := range ac.Processes {
		assert.Equal(t, "/var/lib/mongodb-automation/secrets/certs/"+renewedHash, process.Args26.Get("net.tls.certificateKeyFile").String())
	}
	require.NotNil(t, ac.TLSConfig)
	bundleHash := bundleAfter.Data[util.LatestHashSecretKey]
	require.NotEmpty(t, bundleHash)
	assert.Equal(t, path.Join(util.ConfigMapVolumeCAMountPath, bundleHash), ac.TLSConfig.CAFilePath,
		"the AppDB CA file must follow the published bundle hash so a widened or rotated bundle takes effect")
	assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
	assert.Empty(t, listManagedCertificateObjects(t, ctx, kubernetesClient.NewClient(returning)))
}

func TestAppDBManagedMultiClusterOpsManagerTrust(t *testing.T) {
	t.Setenv(util.OpsManagerMonitorAppDB, "true")
	ctx := t.Context()
	manager, central, r := newMultiClusterManagedAppDB(t)
	manager.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
	r.memberClustersMap["east"] = central
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: manager.GetOpsManagerCA(), Namespace: manager.Namespace, UID: types.UID("managed-om-bundle"),
		OwnerReferences: kube.BaseOwnerReference(manager),
	}, Data: map[string]string{"mms-ca.crt": "OM CA"}}
	require.NoError(t, central.Create(ctx, source))
	reconciler, err := newAppDbMultiReconciler(ctx, central, manager, r.memberClustersMap, zap.S(), nil)
	require.NoError(t, err)
	vars := &env.PodEnvVars{ProjectID: "appdb-project", SSLProjectConfig: env.SSLProjectConfig{SSLMMSCAConfigMap: source.Name, SSLRequireValidMMSServerCertificates: true}}
	status := reconciler.replicateSSLMMSCAConfigMap(ctx, manager, vars, zap.S())
	require.True(t, status.IsOK(), status.Message())
	retained, err := central.GetConfigMap(ctx, client.ObjectKeyFromObject(source))
	require.NoError(t, err)
	assert.Equal(t, source.OwnerReferences, retained.OwnerReferences)
	assert.Equal(t, source.ResourceVersion, retained.ResourceVersion, "AppDB must not rewrite its central OM trust source")
	remote := kubernetesClient.NewClient(r.memberClustersMap["west"])
	copy, err := remote.GetConfigMap(ctx, client.ObjectKeyFromObject(source))
	require.NoError(t, err)
	assert.Equal(t, source.Data, copy.Data)
	assert.Equal(t, certs.ManagedCertificateOwnerLabels(manager), copy.Labels, "AppDB-only member clusters need the UID label the certificate sweep matches")
	assert.Empty(t, copy.OwnerReferences)
	require.NoError(t, deleteManagedCertificateObjectsInCluster(ctx, remote, "west", certs.NewServerCertificateOwner(manager, manager.Spec.Security.ManagedCertificate, certs.ServerCertificateOptions{NamingBase: manager.Name + "-managed-om"}), zap.S()))
	err = remote.Get(ctx, client.ObjectKeyFromObject(source), &corev1.ConfigMap{})
	assert.True(t, apiErrors.IsNotFound(err), "the certificate sweep must remove replicated OM trust")
}

func TestAppDB_MultiCluster(t *testing.T) {
	ctx := context.Background()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2}

	clusterSpecItems := mdbv1.ClusterSpecList{
		{
			ClusterName: memberClusterName,
			Members:     2,
		},
		{
			ClusterName: memberClusterName2,
			Members:     3,
		},
	}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(clusterSpecItems).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBTLSConfig(mdbv1.TLSConfig{
			Enabled:                      true,
			AdditionalCertificateDomains: nil,
			CA:                           "appdb-ca",
		})

	opsManager := builder.Build()
	appdb := *opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	// prepare CA config map in central cluster
	caConfigMapName := createAppDbCAConfigMap(ctx, t, kubeClient, appdb)
	tlsCertSecretName, tlsSecretPemHash := createAppDBTLSCert(ctx, t, kubeClient, appdb)
	pemSecretName := tlsCertSecretName + "-pem"

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, zap.S(), omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)
	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// requeue is true to add monitoring
	assert.True(t, reconcileResult.RequeueAfter > 0)

	centralClusterChecks := newClusterChecks(t, centralClusterName, -1, opsManager.Namespace, kubeClient)
	// secrets and config maps created by the operator shouldn't be created in central cluster
	centralClusterChecks.checkSecretNotFound(ctx, appdb.AutomationConfigSecretName())
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.AutomationConfigConfigMapName())
	centralClusterChecks.checkSecretNotFound(ctx, monitoringAutomationConfigSecretName(&appdb))
	centralClusterChecks.checkConfigMapNotFound(ctx, monitoringAutomationConfigConfigMapName(&appdb))
	centralClusterChecks.checkSecretNotFound(ctx, pemSecretName)
	centralClusterChecks.checkTLSCAConfigMap(ctx, caConfigMapName)
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.ProjectIDConfigMapName())

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName])
		memberClusterChecks.checkAutomationConfigSecret(ctx, appdb.AutomationConfigSecretName())
		memberClusterChecks.checkAutomationConfigConfigMap(ctx, appdb.AutomationConfigConfigMapName())
		memberClusterChecks.checkSecretNotFound(ctx, monitoringAutomationConfigSecretName(&appdb))
		memberClusterChecks.checkConfigMapNotFound(ctx, monitoringAutomationConfigConfigMapName(&appdb))
		memberClusterChecks.checkTLSCAConfigMap(ctx, caConfigMapName)
		// TLS secret should not be replicated, only PEM secret
		memberClusterChecks.checkSecretNotFound(ctx, tlsCertSecretName)
		memberClusterChecks.checkPEMSecret(ctx, pemSecretName, tlsSecretPemHash)

		memberClusterChecks.checkStatefulSet(ctx, opsManager.Spec.AppDB.NameForCluster(reconciler.helper.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
		memberClusterChecks.checkPerPodServices(ctx, opsManager.Spec.AppDB.NameForCluster(reconciler.helper.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
	}

	// OM API Key secret is required for enabling monitoring to OM
	createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

	// reconcile to add monitoring
	reconcileResult, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	// monitoring here is configured, everything should be replicated

	// we create project id and agent key resources only in member clusters
	centralClusterChecks.checkConfigMapNotFound(ctx, appdb.ProjectIDConfigMapName())
	agentAPIKey := ""
	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName])
		projectID := memberClusterChecks.checkProjectIDConfigMap(ctx, appdb.ProjectIDConfigMapName())
		agentAPIKeyFromSecret := memberClusterChecks.checkAgentAPIKeySecret(ctx, projectID)
		assert.NotEmpty(t, agentAPIKeyFromSecret)
		if agentAPIKey == "" {
			// save the value to check if all member clusters contain the same value
			agentAPIKey = agentAPIKeyFromSecret
		}
		assert.Equal(t, agentAPIKey, agentAPIKeyFromSecret)
	}
}

func agentAPIKeySecretName(projectID string) string {
	return fmt.Sprintf("%s-group-secret", projectID)
}

func TestAppDB_MultiCluster_AutomationConfig(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2, memberClusterName3}

	builder := DefaultOpsManagerBuilder().
		SetName("om").
		SetNamespace("ns").
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
		},
		).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	globalClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	err := createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// requeue is true to add monitoring
	assert.True(t, reconcileResult.RequeueAfter > 0)

	// OM API Key secret is required for enabling monitoring to OM
	createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

	// reconcile to add monitoring
	reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)
	reconcileResult, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	t.Run("check expected hostnames", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     1,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
		}
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 1, log)
	})

	t.Run("scale down second cluster to zero before removal", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     0,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
		}
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 1, log)
	})

	t.Run("remove second cluster and add new one", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-2-0",
		}

		// 2 reconciles, remove 1 member from memberClusterName2 and add one from memberClusterName3
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 2, log)
	})

	t.Run("add second cluster back to check indexes are preserved with different clusterSpecItem order", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-2-0",
		}

		// 2 reconciles to add 2 members of memberClusterName2
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 2, log)
	})

	t.Run("remove second cluster from global cluster to simulate full-cluster failure", func(t *testing.T) {
		globalMemberClusterMapWithoutCluster2 := getFakeMultiClusterMapWithClusters([]string{memberClusterName, memberClusterName3}, omConnectionFactory)
		// no changes to clusterSpecItems, nothing should be scaled, processes should be the same
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-2-0",
		}

		// nothing to be scaled
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, false, expectedHostnames, expectedProcessNames, log)

		// Scale "failed" second cluster down to zero before removal
		clusterSpecItems = mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     0,
			},
		}

		expectedHostnames = []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames = []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-2-0",
		}

		// one process from memberClusterName2 should be removed
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, true, expectedHostnames, expectedProcessNames, log)

		expectedHostnames = []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames = []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-2-0",
		}

		// the last process from memberClusterName2 should be removed
		// this should be final reconcile
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, false, expectedHostnames, expectedProcessNames, log)

		// memberClusterName2 is removed
		clusterSpecItems = mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
		}

		expectedHostnames = []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames = []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-2-0",
		}

		// nothing to be scaled
		reconcileAppDBOnceAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalMemberClusterMapWithoutCluster2, memberClusterName, clusterSpecItems, false, expectedHostnames, expectedProcessNames, log)
	})
	t.Run("add second cluster back to check indexes are preserved with different clusterSpecItem order", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-2-0-svc.ns.svc.cluster.local",
		}

		expectedProcessNames := []string{
			"om-db-0-0",
			"om-db-0-1",
			"om-db-1-0",
			"om-db-1-1",
			"om-db-2-0",
		}

		// 2 reconciles to add 2 members of memberClusterName2
		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, opsManager, globalClusterMap, memberClusterName, clusterSpecItems, expectedHostnames, expectedProcessNames, 2, log)
	})
}

func assertExpectedHostnamesAndPreferred(t *testing.T, omConnection *om.MockedOmConnection, expectedHostnames []string) {
	ctx := t.Context()
	hosts, _ := omConnection.GetHosts(ctx)
	assert.Equal(t, expectedHostnames, util.Transform(hosts.Results, func(obj host.Host) string {
		return obj.Hostname
	}), "the AppDB hosts should have been added")

	preferredHostnames, _ := omConnection.GetPreferredHostnames(ctx, omConnection.AgentAPIKey)
	assert.Equal(t, expectedHostnames, util.Transform(preferredHostnames, func(obj om.PreferredHostname) string {
		return obj.Value
	}), "the AppDB preferred hostnames should have been added")
}

func assertExpectedProcesses(ctx context.Context, t *testing.T, memberClusterName string, reconciler *ReconcileAppDbReplicaSet, opsManager *omv1.MongoDBOpsManager, expectedHostnames []string, expectedProcessNames []string) {
	ac, err := automationconfig.ReadFromSecret(ctx, reconciler.helper.getMemberCluster(memberClusterName).SecretClient, types.NamespacedName{
		Namespace: opsManager.GetNamespace(),
		Name:      opsManager.Spec.AppDB.AutomationConfigSecretName(),
	})
	require.NoError(t, err)

	assert.Equal(t, expectedHostnames, util.Transform(ac.Processes, func(obj automationconfig.Process) string {
		return obj.HostName
	}))
	assert.Equal(t, expectedProcessNames, util.Transform(ac.Processes, func(obj automationconfig.Process) string {
		return obj.Name
	}))

	assert.Equal(t, expectedHostnames, reconciler.getCurrentStatefulsetHostnames(opsManager))
}

func reconcileAppDBOnceAndCheckExpectedProcesses(ctx context.Context, t *testing.T, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, memberClusterName string, clusterSpecItems mdbv1.ClusterSpecList, expectedRequeue bool, expectedHostnames []string, expectedProcessNames []string, log *zap.SugaredLogger) {
	opsManager.Spec.AppDB.ClusterSpecList = clusterSpecItems

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactoryFunc)
	require.NoError(t, err)
	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	if expectedRequeue {
		// we're expected to scale one by one for expectedReconciles count
		require.Greater(t, reconcileResult.RequeueAfter, time.Duration(0))
	} else {
		require.Equal(t, util.TWENTY_FOUR_HOURS, reconcileResult.RequeueAfter)
	}

	assertExpectedProcesses(ctx, t, memberClusterName, reconciler, opsManager, expectedHostnames, expectedProcessNames)
}

func reconcileAppDBForExpectedNumberOfTimes(ctx context.Context, t *testing.T, kubeClient client.Client, omConnectionFactoryFunc *om.ConnectionFactory, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, clusterSpecItems mdbv1.ClusterSpecList, expectedReconciles int, log *zap.SugaredLogger) *ReconcileAppDbReplicaSet {
	opsManager.Spec.AppDB.ClusterSpecList = clusterSpecItems

	var reconciler *ReconcileAppDbReplicaSet
	var err error
	for i := range expectedReconciles {
		reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, *omConnectionFactoryFunc)
		require.NoError(t, err)
		reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)

		// when scaling only the last final reconcile will be without requeueAfter
		if i < expectedReconciles-1 {
			// we're expected to scale one by one for expectedReconciles count
			require.Greater(t, reconcileResult.RequeueAfter, time.Duration(0), "failed in reconcile %d", i)
		} else {
			ok, _ := workflow.OK().ReconcileResult()
			require.Equal(t, ok, reconcileResult, "failed in reconcile %d", i)
		}
	}
	return reconciler
}

func reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedProcesses(ctx context.Context, t *testing.T, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, memberClusterName string, clusterSpecItems mdbv1.ClusterSpecList, expectedHostnames []string, expectedProcessNames []string, expectedReconciles int, log *zap.SugaredLogger) {
	reconciler := reconcileAppDBForExpectedNumberOfTimes(ctx, t, kubeClient, &omConnectionFactoryFunc, opsManager, memberClusterMap, clusterSpecItems, expectedReconciles, log)

	assertExpectedProcesses(ctx, t, memberClusterName, reconciler, opsManager, expectedHostnames, expectedProcessNames)
}

func reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedHostnames(ctx context.Context, t *testing.T, kubeClient client.Client, omConnectionFactoryFunc om.ConnectionFactory, omConnectionFactory *om.CachedOMConnectionFactory, opsManager *omv1.MongoDBOpsManager, memberClusterMap map[string]client.Client, clusterSpecItems mdbv1.ClusterSpecList, expectedHostnames []string, expectedReconciles int, log *zap.SugaredLogger) {
	reconcileAppDBForExpectedNumberOfTimes(ctx, t, kubeClient, &omConnectionFactoryFunc, opsManager, memberClusterMap, clusterSpecItems, expectedReconciles, log)

	assertExpectedHostnamesAndPreferred(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection), expectedHostnames)
}

func makeClusterSpecList(clusters ...string) mdbv1.ClusterSpecList {
	var clusterSpecItems mdbv1.ClusterSpecList
	for _, clusterName := range clusters {
		clusterSpecItems = append(clusterSpecItems, mdbv1.ClusterSpecItem{ClusterName: clusterName, Members: 1})
	}
	return clusterSpecItems
}

func TestNewAppDBReconcilerHelper(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"

	memberClusterNames := func(helper *AppDBReconcilerHelper) []string {
		return util.Transform(helper.GetHealthyMemberClusters(), func(m multicluster.MemberCluster) string {
			return m.Name
		})
	}

	t.Run("multi-cluster: topology is read from the deployment state, including removed clusters", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().
			SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2, memberClusterName3)).
			SetAppDbMembers(0).
			SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
			Build()
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
		memberClusterMap := getFakeMultiClusterMapWithClusters([]string{memberClusterName1, memberClusterName2, memberClusterName3}, omConnectionFactory)

		// deploy with all three clusters to persist the deployment state
		reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
		require.NoError(t, err)
		// simulate that member-cluster-3 had been deployed with one member before being removed from the spec
		reconciler.helper.deploymentState.LastAppliedMemberSpec = map[string]int{memberClusterName3: 1}
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))

		// member-cluster-3 is removed from the spec
		opsManager.Spec.AppDB.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName2)

		helper, err := NewReadOnlyAppDBReconcilerHelper(ctx, opsManager, NewReconcileCommonController(ctx, kubeClient, nil), memberClusterMap, log)
		require.NoError(t, err)

		assert.Equal(t, []string{memberClusterName1, memberClusterName2, memberClusterName3}, memberClusterNames(helper))
	})

	t.Run("multi-cluster: legacy config maps are used when the deployment state does not exist, without persisting it", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().
			SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1)).
			SetAppDbMembers(0).
			SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
			Build()
		appdb := *opsManager.Spec.AppDB
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
		memberClusterMap := getFakeMultiClusterMapWithClusters([]string{memberClusterName1, memberClusterName2}, omConnectionFactory)

		// legacy state: member-cluster-2 was deployed with one member, but there is no deployment state config map
		err := kubeClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: appdb.ClusterMappingConfigMapName(), Namespace: appdb.Namespace},
			Data:       map[string]string{memberClusterName1: "0", memberClusterName2: "1"},
		})
		require.NoError(t, err)
		err = kubeClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: appdb.LastAppliedMemberSpecConfigMapName(), Namespace: appdb.Namespace},
			Data:       map[string]string{memberClusterName2: "1"},
		})
		require.NoError(t, err)

		helper, err := NewReadOnlyAppDBReconcilerHelper(ctx, opsManager, NewReconcileCommonController(ctx, kubeClient, nil), memberClusterMap, log)
		require.NoError(t, err)

		assert.Equal(t, []string{memberClusterName1, memberClusterName2}, memberClusterNames(helper))

		// the helper is read-only: the deployment state config map must not have been created
		err = kubeClient.Get(ctx, kube.ObjectKey(appdb.Namespace, appdb.Name()+"-state"), &corev1.ConfigMap{})
		assert.True(t, apiErrors.IsNotFound(err))
	})

	t.Run("multi-cluster: the persisting constructor writes the deployment state", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().
			SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
			SetAppDbMembers(0).
			SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
			Build()
		appdb := *opsManager.Spec.AppDB
		kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
		memberClusterMap := getFakeMultiClusterMapWithClusters([]string{memberClusterName1, memberClusterName2}, omConnectionFactory)

		helper, err := NewAppDBReconcilerHelper(ctx, opsManager, NewReconcileCommonController(ctx, kubeClient, nil), memberClusterMap, log)
		require.NoError(t, err)

		assert.Equal(t, []string{memberClusterName1, memberClusterName2}, memberClusterNames(helper))
		checkClusterMapping(ctx, t, kubeClient, appdb.Namespace, appdb.Name(), map[string]int{memberClusterName1: 0, memberClusterName2: 1})
	})

	t.Run("single-cluster: the topology is the central cluster", func(t *testing.T) {
		opsManager := DefaultOpsManagerBuilder().Build()
		kubeClient, _ := mock.NewDefaultFakeClient(opsManager)

		helper, err := NewReadOnlyAppDBReconcilerHelper(ctx, opsManager, NewReconcileCommonController(ctx, kubeClient, nil), nil, log)
		require.NoError(t, err)

		assert.Equal(t, []string{multicluster.LegacyCentralClusterName}, memberClusterNames(helper))
	})
}

func TestAppDB_MultiCluster_ClusterMapping(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	memberClusterName4 := "member-cluster-4"
	memberClusterName5 := "member-cluster-5"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2, memberClusterName3, memberClusterName4, memberClusterName5}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	appdb := *opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	// prepare CA config map in central cluster
	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	t.Run("check mapping cm has been created", func(t *testing.T) {
		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		})
	})

	t.Run("deployment state config map should be recreated after deletion", func(t *testing.T) {
		deleteDeploymentStateConfigMap(ctx, t, kubeClient, appdb)
		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		})
	})

	t.Run("config map is updated after adding new cluster", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName2, memberClusterName3)
		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		})
	})

	t.Run("mapping is preserved if cluster is removed", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3)

		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		})
	})

	t.Run("new cluster is assigned new index instead of the next one", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName1, memberClusterName3, memberClusterName4)
		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
		})
	})

	t.Run("empty cluster spec list does not change mapping", func(t *testing.T) {
		appdb.ClusterSpecList = mdbv1.ClusterSpecList{}

		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
		})
	})

	t.Run("new cluster alone will get new index", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName5)

		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
			memberClusterName5: 4,
		})
	})

	t.Run("defining clusters again will get their old indexes, order doesn't matter", func(t *testing.T) {
		appdb.ClusterSpecList = makeClusterSpecList(memberClusterName4, memberClusterName2, memberClusterName3, memberClusterName1)

		reconciler.helper.updateMemberClusterMapping(appdb)
		require.NoError(t, reconciler.helper.stateStore.WriteState(ctx, reconciler.helper.deploymentState, log))
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
			memberClusterName4: 3,
			memberClusterName5: 4,
		})
	})
}

func TestAppDB_MultiCluster_ClusterMappingMigrationToDeploymentState(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2, memberClusterName3}

	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	lastAppliedMongoDBVersion := "5.0"
	opsManager.Annotations = map[string]string{annotations.LastAppliedMongoDBVersion: lastAppliedMongoDBVersion}
	appdb := *opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	legacyCM := configmap.Builder().
		SetName(appdb.Name() + "-cluster-mapping").
		SetNamespace(appdb.Namespace).
		SetData(map[string]string{
			memberClusterName3: "2",
			memberClusterName1: "1",
			memberClusterName2: "0",
		}).Build()
	require.NoError(t, kubeClient.Create(ctx, &legacyCM))

	legacyLastAppliedSpecCM := configmap.Builder().
		SetName(appdb.Name() + "-member-spec").
		SetNamespace(appdb.Namespace).
		SetData(map[string]string{
			memberClusterName3: "3",
			memberClusterName1: "1",
			memberClusterName2: "2",
		}).Build()
	require.NoError(t, kubeClient.Create(ctx, &legacyLastAppliedSpecCM))

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	t.Run("check legacy cm should be migrated to the new deployment state", func(t *testing.T) {
		checkClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 1,
			memberClusterName2: 0,
			memberClusterName3: 2,
		})
		checkLastAppliedMemberSpec(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), map[string]int{
			memberClusterName1: 1,
			memberClusterName3: 3,
			memberClusterName2: 2,
		})
		checkLastAppliedMongoDBVersion(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), lastAppliedMongoDBVersion)
	})
}

// This test ensures that we update legacy Config Maps on top of the new Deployment State
func TestAppDB_MultiCluster_KeepUpdatingLegacyState(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2}

	expectedLastAppliedMongoDBVersion := "6.0.0"
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(makeClusterSpecList(memberClusterName1, memberClusterName2)).
		SetAppDbMembers(1).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDbVersion(expectedLastAppliedMongoDBVersion)

	opsManager := builder.Build()
	appdb := *opsManager.Spec.AppDB
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	_, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	t.Run("check that legacy config maps are created based on deployment state", func(t *testing.T) {
		expectedClusterMapping := map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		}
		checkLegacyClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), expectedClusterMapping)

		expectedLastAppliedMemberSpec := map[string]int{
			memberClusterName1: 1,
			memberClusterName2: 1,
		}
		checkLegacyLastAppliedMemberSpec(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), expectedLastAppliedMemberSpec)
		checkLegacyLastAppliedMongoDBVersion(ctx, t, reconciler.helper.centralClient, opsManager.Namespace, opsManager.GetName(), expectedLastAppliedMongoDBVersion)
	})

	t.Run("check that legacy config maps are updated on reconcile when scaling", func(t *testing.T) {
		// Update the cluster spec lists and perform new reconcile
		opsManager.Spec.AppDB.ClusterSpecList = []mdbv1.ClusterSpecItem{
			{
				ClusterName: memberClusterName1,
				Members:     1,
			},
			{
				ClusterName: memberClusterName2,
				Members:     0,
			},
		}
		reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)

		expectedClusterMapping := map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
		}
		checkLegacyClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), expectedClusterMapping)

		expectedLastAppliedMemberSpec := map[string]int{
			memberClusterName1: 1,
			memberClusterName2: 0,
		}
		checkLegacyLastAppliedMemberSpec(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), expectedLastAppliedMemberSpec)
		checkLegacyLastAppliedMongoDBVersion(ctx, t, reconciler.helper.centralClient, opsManager.Namespace, opsManager.GetName(), expectedLastAppliedMongoDBVersion)
	})

	t.Run("check that legacy config maps are updated on reconcile when removing a cluster", func(t *testing.T) {
		// Remove a cluster from the cluster spec list and add a new one
		opsManager.Spec.AppDB.ClusterSpecList = []mdbv1.ClusterSpecItem{
			{
				ClusterName: memberClusterName1,
				Members:     1,
			},
			{
				ClusterName: memberClusterName3,
				Members:     1,
			},
		}
		reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, log, omConnectionFactory.GetConnectionFunc)
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)

		// Cluster 2 is not in cluster spec list anymore, but the reconciler should keep it in the index map
		expectedClusterMapping := map[string]int{
			memberClusterName1: 0,
			memberClusterName2: 1,
			memberClusterName3: 2,
		}
		checkLegacyClusterMapping(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), expectedClusterMapping)

		expectedLastAppliedMemberSpec := map[string]int{
			memberClusterName1: 1,
			memberClusterName2: 0,
			memberClusterName3: 1,
		}
		checkLegacyLastAppliedMemberSpec(ctx, t, reconciler.helper.centralClient, appdb.Namespace, appdb.Name(), expectedLastAppliedMemberSpec)
		checkLegacyLastAppliedMongoDBVersion(ctx, t, reconciler.helper.centralClient, opsManager.Namespace, opsManager.GetName(), expectedLastAppliedMongoDBVersion)
	})
}

func deleteDeploymentStateConfigMap(ctx context.Context, t *testing.T, kubeClient client.Client, appdb omv1.AppDBSpec) {
	cm := corev1.ConfigMap{}
	err := kubeClient.Get(ctx, kube.ObjectKey(appdb.Namespace, appdb.Name()+"-state"), &cm)
	require.NoError(t, err)
	err = kubeClient.Delete(ctx, &cm)
	require.NoError(t, err)
}

func readDeploymentState[T any](ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string) *T {
	cm := corev1.ConfigMap{}
	err := c.Get(ctx, kube.ObjectKey(namespace, resourceName+"-state"), &cm)
	require.NoError(t, err)

	stateStruct := new(T)
	require.NoError(t, json.Unmarshal([]byte(cm.Data["state"]), stateStruct))

	return stateStruct
}

func checkClusterMapping(ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string, expectedMapping map[string]int) {
	deploymentState := readDeploymentState[AppDBDeploymentState](ctx, t, c, namespace, resourceName)
	assert.Equal(t, expectedMapping, deploymentState.ClusterMapping)
}

func checkLastAppliedMemberSpec(ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string, expectedMemberSpec map[string]int) {
	deploymentState := readDeploymentState[AppDBDeploymentState](ctx, t, c, namespace, resourceName)
	assert.Equal(t, expectedMemberSpec, deploymentState.LastAppliedMemberSpec)
}

func checkLastAppliedMongoDBVersion(ctx context.Context, t *testing.T, c client.Client, namespace string, resourceName string, expectedVersion string) {
	deploymentState := readDeploymentState[AppDBDeploymentState](ctx, t, c, namespace, resourceName)
	assert.Equal(t, expectedVersion, deploymentState.LastAppliedMongoDBVersion)
}

func checkLegacyClusterMapping(ctx context.Context, t *testing.T, client client.Client, namespace, appdbName string, expectedData map[string]int) {
	cm := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: appdbName + "-cluster-mapping", Namespace: namespace}, cm)
	require.NoError(t, err)
	for k, v := range expectedData {
		assert.Equal(t, strconv.Itoa(v), cm.Data[k])
	}
}

func checkLegacyLastAppliedMemberSpec(ctx context.Context, t *testing.T, client client.Client, namespace, appdbName string, expectedData map[string]int) {
	cm := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Name: appdbName + "-member-spec", Namespace: namespace}, cm)
	require.NoError(t, err)
	for k, v := range expectedData {
		assert.Equal(t, strconv.Itoa(v), cm.Data[k])
	}
}

func checkLegacyLastAppliedMongoDBVersion(ctx context.Context, t *testing.T, client client.Client, namespace, omName, expectedVersion string) {
	opsManager := &omv1.MongoDBOpsManager{}
	err := client.Get(ctx, types.NamespacedName{Name: omName, Namespace: namespace}, opsManager)
	require.NoError(t, err)
	assert.Equal(t, expectedVersion, opsManager.Annotations[annotations.LastAppliedMongoDBVersion])
}

func createOMAPIKeySecret(ctx context.Context, t *testing.T, secretClient secrets.SecretClient, opsManager *omv1.MongoDBOpsManager) {
	APIKeySecretName, err := opsManager.APIKeySecretName(ctx, secretClient, "")
	assert.NoError(t, err)

	data := map[string]string{
		util.OmPublicApiKey: "publicApiKey",
		util.OmPrivateKey:   "privateApiKey",
	}

	apiKeySecret := secret.Builder().
		SetNamespace(operatorNamespace()).
		SetName(APIKeySecretName).
		SetStringMapToData(data).
		SetLabels(map[string]string{omv1.OpsManagerNamespaceLabel: opsManager.Namespace}).
		Build()

	err = secretClient.CreateSecret(ctx, apiKeySecret)
	require.NoError(t, err)
}

func createAppDbCAConfigMap(ctx context.Context, t *testing.T, k8sClient client.Client, appDBSpec omv1.AppDBSpec) string {
	cert, _ := createMockCertAndKeyBytes()
	cm := configmap.Builder().
		SetName(appDBSpec.GetCAConfigMapName()).
		SetNamespace(appDBSpec.Namespace).
		SetDataField("ca-pem", string(cert)).
		Build()

	err := k8sClient.Create(ctx, &cm)
	require.NoError(t, err)

	return appDBSpec.GetCAConfigMapName()
}

func createAppDBTLSCert(ctx context.Context, t *testing.T, k8sClient client.Client, appDBSpec omv1.AppDBSpec) (string, string) {
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appDBSpec.GetTlsCertificatesSecretName(),
			Namespace: appDBSpec.Namespace,
		},
		Type: corev1.SecretTypeTLS,
	}

	certs := map[string][]byte{}
	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()

	tlsSecret.Data = certs
	err := k8sClient.Create(ctx, tlsSecret)
	require.NoError(t, err)

	pemHash := enterprisepem.ReadHashFromData(secrets.DataToStringData(tlsSecret.Data), zap.S())
	require.NotEmpty(t, pemHash)

	return tlsSecret.Name, pemHash
}

func TestAppDB_MultiCluster_ReconcilerFailsWhenThereIsNoClusterListConfigured(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: "a",
				Members:     2,
			},
		}).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)
	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	_, err := newAppDbReconciler(ctx, kubeClient, opsManager, omConnectionFactory.GetConnectionFunc, zap.S())
	assert.Error(t, err)
}

func TestAppDBMultiClusterRemoveResources(t *testing.T) {
	ctx := context.Background()
	builder := DefaultOpsManagerBuilder().
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: "a",
				Members:     2,
			},
			{
				ClusterName: "b",
				Members:     2,
			},
			{
				ClusterName: "c",
				Members:     1,
			},
		}).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	clusters = []string{"a", "b", "c"}
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters, omConnectionFactory)
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, memberClusterMap, omConnectionFactory, architectures.NonStatic)

	// create opsmanager reconciler
	appDBReconciler, _ := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, zap.S(), omConnectionFactory.GetConnectionFunc)

	// initially requeued as monitoring needs to be configured
	_, err := appDBReconciler.ReconcileAppDB(ctx, opsManager)
	assert.NoError(t, err)

	// check AppDB statefulset exists in cluster "a" and cluster "b"
	for clusterIdx, clusterSpecItem := range opsManager.Spec.AppDB.ClusterSpecList {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName])

		memberClusterChecks.checkStatefulSet(ctx, opsManager.Spec.AppDB.NameForCluster(appDBReconciler.helper.getMemberClusterIndex(clusterSpecItem.ClusterName)), clusterSpecItem.Members)
	}

	// delete the OM resource
	reconciler.OnDelete(ctx, opsManager, zap.S())
	assert.Zero(t, len(reconciler.resourceWatcher.GetWatchedResources()))

	// assert STS objects in member cluster
	for clusterIdx, clusterSpecItem := range opsManager.Spec.AppDB.ClusterSpecList {
		memberClusterChecks := newClusterChecks(t, clusterSpecItem.ClusterName, clusterIdx, opsManager.Namespace, memberClusterMap[clusterSpecItem.ClusterName])

		memberClusterChecks.checkStatefulSetDoesNotExist(ctx, opsManager.Spec.AppDB.NameForCluster(appDBReconciler.helper.getMemberClusterIndex(clusterSpecItem.ClusterName)))
	}
}

func TestAppDBMultiClusterMonitoringHostnames(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName, memberClusterName2, memberClusterName3}

	builder := DefaultOpsManagerBuilder().
		SetName("om").
		SetNamespace("ns").
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     3,
			},
			{
				ClusterName: memberClusterName3,
				Members:     2,
			},
		},
		).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	globalClusterMap := getFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	hostnames := reconciler.generateProcessHostnames(opsManager)

	expectedHostnames := []string{
		"om-db-0-0-svc.ns.svc.cluster.local",
		"om-db-0-1-svc.ns.svc.cluster.local",
		"om-db-1-0-svc.ns.svc.cluster.local",
		"om-db-1-1-svc.ns.svc.cluster.local",
		"om-db-1-2-svc.ns.svc.cluster.local",
		"om-db-2-0-svc.ns.svc.cluster.local",
		"om-db-2-1-svc.ns.svc.cluster.local",
	}

	assert.Equal(t, expectedHostnames, hostnames)

	/* Default external domain */

	opsManager.Spec.AppDB.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{
		ExternalDomain: new("custom.domain"),
	}

	hostnames = reconciler.generateProcessHostnames(opsManager)

	expectedHostnames = []string{
		"om-db-0-0.custom.domain",
		"om-db-0-1.custom.domain",
		"om-db-1-0.custom.domain",
		"om-db-1-1.custom.domain",
		"om-db-1-2.custom.domain",
		"om-db-2-0.custom.domain",
		"om-db-2-1.custom.domain",
	}

	assert.Equal(t, expectedHostnames, hostnames)

	/* Per cluster external domain mixed with default domain */

	opsManager.Spec.AppDB.ClusterSpecList[0].ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{
		ExternalDomain: new("cluster0.domain"),
	}
	opsManager.Spec.AppDB.ClusterSpecList[2].ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{
		ExternalDomain: new("cluster2.domain"),
	}

	hostnames = reconciler.generateProcessHostnames(opsManager)

	expectedHostnames = []string{
		"om-db-0-0.cluster0.domain",
		"om-db-0-1.cluster0.domain",
		"om-db-1-0.custom.domain",
		"om-db-1-1.custom.domain",
		"om-db-1-2.custom.domain",
		"om-db-2-0.cluster2.domain",
		"om-db-2-1.cluster2.domain",
	}

	assert.Equal(t, expectedHostnames, hostnames)
}

func TestAppDBMultiClusterTryConfigureMonitoring(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2, memberClusterName3}

	builder := DefaultOpsManagerBuilder().
		SetName("om").
		SetNamespace("ns").
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     2,
			},
		},
		).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
	globalClusterMap := getAppDBFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	err := createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	// requeue is true to add monitoring
	assert.True(t, reconcileResult.RequeueAfter > 0)

	// OM API Key secret is required for enabling monitoring to OM
	createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

	t.Run("check expected hostnames", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     2,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
		}

		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedHostnames(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, omConnectionFactory, opsManager, globalClusterMap, clusterSpecItems, expectedHostnames, 1, log)

		// teardown
		clearHostsAndPreferredHostnames(omConnectionFactory)
	})
	t.Run("Add second cluster", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     3,
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-1-2-svc.ns.svc.cluster.local",
		}

		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedHostnames(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, omConnectionFactory, opsManager, globalClusterMap, clusterSpecItems, expectedHostnames, 3, log)

		// teardown
		clearHostsAndPreferredHostnames(omConnectionFactory)
	})
	t.Run("Add default external domain", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     2,
			},
			{
				ClusterName: memberClusterName2,
				Members:     3,
			},
		}

		opsManager.Spec.AppDB.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("custom.domain")}

		expectedHostnames := []string{
			"om-db-0-0.custom.domain",
			"om-db-0-1.custom.domain",
			"om-db-1-0.custom.domain",
			"om-db-1-1.custom.domain",
			"om-db-1-2.custom.domain",
		}

		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedHostnames(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, omConnectionFactory, opsManager, globalClusterMap, clusterSpecItems, expectedHostnames, 3, log)

		// teardown
		clearHostsAndPreferredHostnames(omConnectionFactory)
		opsManager.Spec.AppDB.ExternalAccessConfiguration = nil
	})
	t.Run("Add external domain for second cluster", func(t *testing.T) {
		clearHostsAndPreferredHostnames(omConnectionFactory)

		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     2,
			},
			{
				ClusterName:                 memberClusterName2,
				Members:                     3,
				ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("cluster-1.domain")},
			},
		}

		expectedHostnames := []string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0.cluster-1.domain",
			"om-db-1-1.cluster-1.domain",
			"om-db-1-2.cluster-1.domain",
		}

		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedHostnames(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, omConnectionFactory, opsManager, globalClusterMap, clusterSpecItems, expectedHostnames, 3, log)

		// teardown
		clearHostsAndPreferredHostnames(omConnectionFactory)
		opsManager.Spec.AppDB.ExternalAccessConfiguration = nil
	})
	t.Run("Add default external domain and custom for second cluster + ", func(t *testing.T) {
		clusterSpecItems := mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     2,
			},
			{
				ClusterName:                 memberClusterName2,
				Members:                     3,
				ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("cluster-1.domain")},
			},
		}

		opsManager.Spec.AppDB.ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{ExternalDomain: new("custom.domain")}

		expectedHostnames := []string{
			"om-db-0-0.custom.domain",
			"om-db-0-1.custom.domain",
			"om-db-1-0.cluster-1.domain",
			"om-db-1-1.cluster-1.domain",
			"om-db-1-2.cluster-1.domain",
		}

		reconcileAppDBForExpectedNumberOfTimesAndCheckExpectedHostnames(ctx, t, kubeClient, omConnectionFactory.GetConnectionFunc, omConnectionFactory, opsManager, globalClusterMap, clusterSpecItems, expectedHostnames, 3, log)

		// teardown
		clearHostsAndPreferredHostnames(omConnectionFactory)
		opsManager.Spec.AppDB.ExternalAccessConfiguration = nil
	})
}

func clearHostsAndPreferredHostnames(omConnectionFactory *om.CachedOMConnectionFactory) {
	// clear preferred hostnames so they can be populated during reconcile
	if omConnection := omConnectionFactory.GetConnection(); omConnection != nil {
		mockedOmConnection := omConnection.(*om.MockedOmConnection)
		mockedOmConnection.PreferredHostnames = make([]om.PreferredHostname, 0)
		mockedOmConnection.ClearHosts()
	}
}

func TestAppDBMultiClusterServiceCreation_WithExternalName(t *testing.T) {
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	memberClusterName3 := "member-cluster-3"
	memberClusters := []string{memberClusterName1, memberClusterName2, memberClusterName3}

	tests := map[string]struct {
		clusterSpecList        mdbv1.ClusterSpecList
		uniformExternalAccess  *mdbv1.ExternalAccessConfiguration
		additionalMongodConfig *mdbv1.AdditionalMongodConfig
		result                 map[string]map[int]corev1.Service
	}{
		"empty external access configured for single pod in first cluster": {
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName:                 memberClusterName1,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{},
					Members:                     1,
				},
			},
			result: map[string]map[int]corev1.Service{
				memberClusterName1: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
								omv1.LabelResourceOwner:        "test-om",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
							},
						},
					},
				},
			},
		},
		"external access configured for two pods in first cluster": {
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: memberClusterName1,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
						ExternalService: mdbv1.ExternalServiceConfiguration{
							SpecWrapper: &v1.ServiceSpecWrapper{
								Spec: corev1.ServiceSpec{
									Type: "LoadBalancer",
									Ports: []corev1.ServicePort{
										{
											Name: "mongodb",
											Port: 27017,
										},
										{
											Name: "backup",
											Port: 27018,
										},
										{
											Name: "testing2",
											Port: 27019,
										},
									},
								},
							},
						},
					},
					Members: 2,
				},
			},
			result: map[string]map[int]corev1.Service{
				memberClusterName1: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
								omv1.LabelResourceOwner:        "test-om",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
								{
									Name: "backup",
									Port: 27018,
								},
								{
									Name: "testing2",
									Port: 27019,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
							},
						},
					},
					1: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-1-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-1",
								omv1.LabelResourceOwner:        "test-om",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
								{
									Name: "backup",
									Port: 27018,
								},
								{
									Name: "testing2",
									Port: 27019,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-1",
							},
						},
					},
				},
			},
		},
		"external domain configured for single pod in first cluster": {
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: memberClusterName1,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
						ExternalDomain: new("custom.domain"),
					},
					Members: 1,
				},
			},
			result: map[string]map[int]corev1.Service{
				memberClusterName1: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
								omv1.LabelResourceOwner:        "test-om",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
							},
						},
					},
				},
			},
		},
		"non default port set in additional mongod config": {
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName:                 memberClusterName1,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{},
					Members:                     1,
				},
			},
			additionalMongodConfig: mdbv1.NewAdditionalMongodConfig("net.port", 27027),
			result: map[string]map[int]corev1.Service{
				memberClusterName1: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
								omv1.LabelResourceOwner:        "test-om",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27027,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
							},
						},
					},
				},
			},
		},
		"external service of NodePort type set in first cluster": {
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: memberClusterName1,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
						ExternalService: mdbv1.ExternalServiceConfiguration{
							SpecWrapper: &v1.ServiceSpecWrapper{
								Spec: corev1.ServiceSpec{
									Type: "NodePort",
									Ports: []corev1.ServicePort{
										{
											Name:     "mongodb",
											Port:     27017,
											NodePort: 30003,
										},
									},
								},
							},
						},
					},
					Members: 1,
				},
			},
			result: map[string]map[int]corev1.Service{
				memberClusterName1: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
								omv1.LabelResourceOwner:        "test-om",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "NodePort",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name:     "mongodb",
									Port:     27017,
									NodePort: 30003,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
							},
						},
					},
				},
			},
		},
		"service with annotations with placeholders": {
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: memberClusterName1,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
						ExternalService: mdbv1.ExternalServiceConfiguration{
							Annotations: map[string]string{
								create.PlaceholderPodIndex:            "{podIndex}",
								create.PlaceholderNamespace:           "{namespace}",
								create.PlaceholderResourceName:        "{resourceName}",
								create.PlaceholderPodName:             "{podName}",
								create.PlaceholderStatefulSetName:     "{statefulSetName}",
								create.PlaceholderExternalServiceName: "{externalServiceName}",
								create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
								create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
								create.PlaceholderClusterName:         "{clusterName}",
								create.PlaceholderClusterIndex:        "{clusterIndex}",
							},
							SpecWrapper: &v1.ServiceSpecWrapper{
								Spec: corev1.ServiceSpec{
									Type: "LoadBalancer",
									Ports: []corev1.ServicePort{
										{
											Name: "mongodb",
											Port: 27017,
										},
									},
								},
							},
						},
					},
					Members: 2,
				},
				{
					ClusterName: memberClusterName2,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
						ExternalService: mdbv1.ExternalServiceConfiguration{
							Annotations: map[string]string{
								create.PlaceholderPodIndex:            "{podIndex}",
								create.PlaceholderNamespace:           "{namespace}",
								create.PlaceholderResourceName:        "{resourceName}",
								create.PlaceholderPodName:             "{podName}",
								create.PlaceholderStatefulSetName:     "{statefulSetName}",
								create.PlaceholderExternalServiceName: "{externalServiceName}",
								create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
								create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
								create.PlaceholderClusterName:         "{clusterName}",
								create.PlaceholderClusterIndex:        "{clusterIndex}",
							},
							SpecWrapper: &v1.ServiceSpecWrapper{
								Spec: corev1.ServiceSpec{
									Type: "LoadBalancer",
									Ports: []corev1.ServicePort{
										{
											Name: "mongodb",
											Port: 27017,
										},
									},
								},
							},
						},
						ExternalDomain: new("custom.domain"),
					},
					Members: 2,
				},
			},
			uniformExternalAccess: &mdbv1.ExternalAccessConfiguration{
				ExternalService: mdbv1.ExternalServiceConfiguration{
					Annotations: map[string]string{
						"test-annotation": "test-placeholder-{podIndex}",
					},
				},
			},
			result: map[string]map[int]corev1.Service{
				memberClusterName1: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
								omv1.LabelResourceOwner:        "test-om",
							},
							Annotations: map[string]string{
								"test-annotation":                     "test-placeholder-0",
								create.PlaceholderPodIndex:            "0",
								create.PlaceholderNamespace:           "my-namespace",
								create.PlaceholderResourceName:        "test-om-db",
								create.PlaceholderStatefulSetName:     "test-om-db-0",
								create.PlaceholderPodName:             "test-om-db-0-0",
								create.PlaceholderExternalServiceName: "test-om-db-0-0-svc-external",
								create.PlaceholderMongodProcessDomain: "my-namespace.svc.cluster.local",
								create.PlaceholderMongodProcessFQDN:   "test-om-db-0-0-svc.my-namespace.svc.cluster.local",
								create.PlaceholderClusterName:         memberClusterName1,
								create.PlaceholderClusterIndex:        "0",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-0",
							},
						},
					},
					1: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-0-1-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-1",
								omv1.LabelResourceOwner:        "test-om",
							},
							Annotations: map[string]string{
								"test-annotation":                     "test-placeholder-1",
								create.PlaceholderPodIndex:            "1",
								create.PlaceholderNamespace:           "my-namespace",
								create.PlaceholderResourceName:        "test-om-db",
								create.PlaceholderStatefulSetName:     "test-om-db-0",
								create.PlaceholderPodName:             "test-om-db-0-1",
								create.PlaceholderExternalServiceName: "test-om-db-0-1-svc-external",
								create.PlaceholderMongodProcessDomain: "my-namespace.svc.cluster.local",
								create.PlaceholderMongodProcessFQDN:   "test-om-db-0-1-svc.my-namespace.svc.cluster.local",
								create.PlaceholderClusterName:         memberClusterName1,
								create.PlaceholderClusterIndex:        "0",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-0-1",
							},
						},
					},
				},
				memberClusterName2: {
					0: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-1-0-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-1-0",
								omv1.LabelResourceOwner:        "test-om",
							},
							Annotations: map[string]string{
								"test-annotation":                     "test-placeholder-0",
								create.PlaceholderPodIndex:            "0",
								create.PlaceholderNamespace:           "my-namespace",
								create.PlaceholderResourceName:        "test-om-db",
								create.PlaceholderStatefulSetName:     "test-om-db-1",
								create.PlaceholderPodName:             "test-om-db-1-0",
								create.PlaceholderExternalServiceName: "test-om-db-1-0-svc-external",
								create.PlaceholderMongodProcessDomain: "custom.domain",
								create.PlaceholderMongodProcessFQDN:   "test-om-db-1-0.custom.domain",
								create.PlaceholderClusterName:         memberClusterName2,
								create.PlaceholderClusterIndex:        "1",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-1-0",
							},
						},
					},
					1: corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "test-om-db-1-1-svc-external",
							Namespace:       "my-namespace",
							ResourceVersion: "1",
							Labels: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-1-1",
								omv1.LabelResourceOwner:        "test-om",
							},
							Annotations: map[string]string{
								"test-annotation":                     "test-placeholder-1",
								create.PlaceholderPodIndex:            "1",
								create.PlaceholderNamespace:           "my-namespace",
								create.PlaceholderResourceName:        "test-om-db",
								create.PlaceholderStatefulSetName:     "test-om-db-1",
								create.PlaceholderPodName:             "test-om-db-1-1",
								create.PlaceholderExternalServiceName: "test-om-db-1-1-svc-external",
								create.PlaceholderMongodProcessDomain: "custom.domain",
								create.PlaceholderMongodProcessFQDN:   "test-om-db-1-1.custom.domain",
								create.PlaceholderClusterName:         memberClusterName2,
								create.PlaceholderClusterIndex:        "1",
							},
						},
						Spec: corev1.ServiceSpec{
							Type:                     "LoadBalancer",
							PublishNotReadyAddresses: true,
							Ports: []corev1.ServicePort{
								{
									Name: "mongodb",
									Port: 27017,
								},
							},
							Selector: map[string]string{
								util.OperatorLabelName:         util.OperatorLabelValue,
								appsv1.StatefulSetPodNameLabel: "test-om-db-1-1",
							},
						},
					},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			opsManagerBuilder := DefaultOpsManagerBuilder().
				SetAppDBClusterSpecList(test.clusterSpecList).
				SetAppDbMembers(0).
				SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
				SetAdditionalMongodbConfig(test.additionalMongodConfig)

			if test.uniformExternalAccess != nil {
				opsManagerBuilder.SetAppDbExternalAccess(*test.uniformExternalAccess)
			}

			opsManager := opsManagerBuilder.Build()

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
			memberClusterMap := getFakeMultiClusterMapWithClusters(memberClusters, omConnectionFactory)

			reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, zap.S(), omConnectionFactory.GetConnectionFunc)
			require.NoError(t, err)

			err = createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
			assert.NoError(t, err)
			reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
			require.NoError(t, err)
			// requeue is true to add monitoring
			assert.True(t, reconcileResult.RequeueAfter > 0)

			clusterSpecList := opsManager.Spec.AppDB.GetClusterSpecList()
			for clusterIdx, item := range clusterSpecList {
				clusterName := item.ClusterName
				c := memberClusterMap[clusterName]
				for podIdx := 0; podIdx < item.Members; podIdx++ {
					svcName := dns.GetMultiExternalServiceName(opsManager.Spec.AppDB.GetName(), clusterIdx, podIdx)
					svcNamespace := opsManager.Namespace

					svcList := corev1.ServiceList{}
					err = c.List(ctx, &svcList)
					assert.NoError(t, err)

					actualSvc := corev1.Service{}

					err = c.Get(ctx, kube.ObjectKey(svcNamespace, svcName), &actualSvc)
					assert.NoError(t, err)

					expectedSvc := test.result[clusterName][podIdx]
					assert.Equal(t, expectedSvc, actualSvc)
				}
			}
		})
	}
}

// TestAppDBMultiCluster_ScaleDown_HostsRemovedFromMonitoring verifies that hosts are removed from monitoring
// when scaling down
func TestAppDBMultiCluster_ScaleDown_HostsRemovedFromMonitoring(t *testing.T) {
	ctx := context.Background()
	log := zap.S()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName1 := "member-cluster-1"
	memberClusterName2 := "member-cluster-2"
	clusters := []string{centralClusterName, memberClusterName1, memberClusterName2}

	builder := DefaultOpsManagerBuilder().
		SetName("om").
		SetNamespace("ns").
		SetAppDBClusterSpecList(mdbv1.ClusterSpecList{
			{
				ClusterName: memberClusterName1,
				Members:     3,
			},
			{
				ClusterName: memberClusterName2,
				Members:     2,
			},
		}).
		SetAppDbMembers(0).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)

	opsManager := builder.Build()
	// Use addOMHosts=false to prevent the interceptor from re-adding hosts when
	// StatefulSets are fetched during scale-down. This allows testing host removal.
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(opsManager).
		WithObjects(mock.GetDefaultResources()...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, false),
		}).Build()
	kubeClient := kubernetesClient.NewClient(fakeClient)
	globalClusterMap := getAppDBFakeMultiClusterMapWithClusters(clusters[1:], omConnectionFactory)

	err := createOpsManagerUserPasswordSecret(ctx, kubeClient, opsManager, opsManagerUserPassword)
	assert.NoError(t, err)

	reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)

	reconcileResult, err := reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)
	assert.True(t, reconcileResult.RequeueAfter > 0)

	createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

	reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
	require.NoError(t, err)
	reconcileResult, err = reconciler.ReconcileAppDB(ctx, opsManager)
	require.NoError(t, err)

	initialHostnames := []string{
		"om-db-0-0-svc.ns.svc.cluster.local",
		"om-db-0-1-svc.ns.svc.cluster.local",
		"om-db-0-2-svc.ns.svc.cluster.local",
		"om-db-1-0-svc.ns.svc.cluster.local",
		"om-db-1-1-svc.ns.svc.cluster.local",
	}

	assertExpectedHostnamesAndPreferred(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection), initialHostnames)

	opsManager.Spec.AppDB.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: memberClusterName1,
			Members:     2,
		},
		{
			ClusterName: memberClusterName2,
			Members:     1,
		},
	}

	// The scaler processes clusters in order (cluster1 first, then cluster2).
	// So cluster1 scales down first (3→2), then cluster2 scales down (2→1).
	expectedHostnamesAfterReconcileStep := [][]string{
		{
			// After first reconcile: cluster1 scaled from 3 to 2 (removed om-db-0-2)
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
		},
		{
			// After second reconcile: cluster2 scaled from 2 to 1 (removed om-db-1-1)
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
			"om-db-1-0-svc.ns.svc.cluster.local",
		},
	}

	for i := range 2 {
		reconciler, err = newAppDbMultiReconciler(ctx, kubeClient, opsManager, globalClusterMap, log, omConnectionFactory.GetConnectionFunc)
		require.NoError(t, err)
		_, err = reconciler.ReconcileAppDB(ctx, opsManager)
		require.NoError(t, err)

		// Ensure the hosts are removed in the right reconcile step.
		omConnection := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
		hosts, _ := omConnection.GetHosts(ctx)
		assert.Equal(t, expectedHostnamesAfterReconcileStep[i], util.Transform(hosts.Results, func(obj host.Host) string {
			return obj.Hostname
		}), "the AppDB hosts should have been removed after scale-down")
	}
}

func TestAppDBBlockNonEmptyClusterSpecItemRemoval(t *testing.T) {
	tests := []struct {
		name            string
		memberClusters  []multicluster.MemberCluster
		clusterSpecList mdbv1.ClusterSpecList
		wantErr         string
	}{
		{
			name: "error when removing non-zero cluster-a",
			memberClusters: []multicluster.MemberCluster{
				{Name: "cluster-a", Replicas: 2},
				{Name: "cluster-b", Replicas: 3},
			},
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: "cluster-b", Members: 3}},
			wantErr:         "Cannot remove member cluster cluster-a with non-zero members count. Please scale down members to zero first",
		},
		{
			name: "no error when removing after scaling cluster-a to zero",
			memberClusters: []multicluster.MemberCluster{
				{Name: "cluster-a", Replicas: 0},
				{Name: "cluster-b", Replicas: 3},
			},
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: "cluster-b", Members: 3}},
			wantErr:         "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reconciler := &ReconcileAppDbReplicaSet{helper: &AppDBReconcilerHelper{memberClusters: tc.memberClusters}}

			err := reconciler.blockNonEmptyClusterSpecItemRemoval(
				omv1.AppDBSpec{
					Topology:        mdbv1.ClusterTopologyMultiCluster,
					ClusterSpecList: tc.clusterSpecList,
				},
			)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Equal(t, tc.wantErr, err.Error())
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestReconcileAppDBBlockNonEmptyClusterSpecItemRemovalIntegration(t *testing.T) {
	ctx := context.Background()
	clusterA := "cluster-a"
	clusterB := "cluster-b"

	tests := []struct {
		name               string
		clusterSpecList    mdbv1.ClusterSpecList
		lastAppliedMembers map[string]int
		wantPhase          status.Phase
		wantMessage        string
	}{
		{
			name:            "error when removing non-zero cluster-a",
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: clusterB, Members: 1}},
			lastAppliedMembers: map[string]int{
				clusterB: 1,
				clusterA: 2,
			},
			wantPhase:   status.PhaseFailed,
			wantMessage: "Cannot remove member cluster cluster-a with non-zero members count. Please scale down members to zero first",
		},
		{
			name:            "no error when removing after scaling cluster-a to zero",
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: clusterB, Members: 1}},
			lastAppliedMembers: map[string]int{
				clusterB: 1,
				clusterA: 0,
			},
			wantPhase: status.PhaseRunning,
		},
		{
			name:            "no error when there is no change in the spec",
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: clusterB, Members: 1}, {ClusterName: clusterA, Members: 2}},
			lastAppliedMembers: map[string]int{
				clusterB: 1,
				clusterA: 2,
			},
			wantPhase: status.PhaseRunning,
		},
		{
			name:            "no error when scaling up",
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: clusterB, Members: 1}, {ClusterName: clusterA, Members: 3}},
			lastAppliedMembers: map[string]int{
				clusterB: 1,
				clusterA: 2,
			},
			wantPhase: status.PhaseRunning,
		},
		{
			name:            "no error when scaling down",
			clusterSpecList: mdbv1.ClusterSpecList{{ClusterName: clusterB, Members: 1}, {ClusterName: clusterA, Members: 1}},
			lastAppliedMembers: map[string]int{
				clusterB: 1,
				clusterA: 2,
			},
			wantPhase: status.PhaseRunning,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opsManager := DefaultOpsManagerBuilder().
				SetName("om").
				SetNamespace("ns").
				SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
				SetAppDBClusterSpecList(tc.clusterSpecList).
				Build()

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(opsManager)
			memberClusterMap := getFakeMultiClusterMapWithClusters([]string{clusterA, clusterB}, omConnectionFactory)

			state := AppDBDeploymentState{
				CommonDeploymentState: CommonDeploymentState{ClusterMapping: map[string]int{
					clusterB: 0,
					clusterA: 1,
				}},
				LastAppliedMemberSpec: tc.lastAppliedMembers,
			}
			stateBytes, err := json.Marshal(state)
			require.NoError(t, err)

			stateCM := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-state", opsManager.Spec.AppDB.Name()),
					Namespace: opsManager.Spec.AppDB.Namespace,
				},
				Data: map[string]string{stateKey: string(stateBytes)},
			}
			require.NoError(t, kubeClient.Create(ctx, &stateCM))

			reconciler, err := newAppDbMultiReconciler(ctx, kubeClient, opsManager, memberClusterMap, zap.S(), omConnectionFactory.GetConnectionFunc)
			require.NoError(t, err)

			createOMAPIKeySecret(ctx, t, reconciler.SecretClient, opsManager)

			_, err = reconciler.ReconcileAppDB(ctx, opsManager)
			require.NoError(t, err)

			require.Equal(t, tc.wantPhase, opsManager.Status.AppDbStatus.Phase)
			require.Equal(t, tc.wantMessage, opsManager.Status.AppDbStatus.Message)
		})
	}
}

// TestManagedAppDBCABundleHashMultiCluster checks that a multi-cluster managed AppDB reports the
// published CA bundle hash, and that the internal and the referenced (external) reconcilers read the
// same bundle because the reference carries the internal AppDB's name.
func TestManagedAppDBCABundleHashMultiCluster(t *testing.T) {
	ctx := t.Context()
	manager, central, r := newMultiClusterManagedAppDB(t)
	bundleName := certs.ManagedServerCABundleConfigMapName(manager.Spec.AppDB.Name())
	require.NoError(t, central.CreateConfigMap(ctx, configmap.Builder().
		SetName(bundleName).
		SetNamespace(manager.Namespace).
		SetData(map[string]string{util.LatestHashSecretKey: "bundle-hash"}).
		Build()))

	internal, err := newAppDbMultiReconciler(ctx, central, manager, r.memberClustersMap, zap.S(), nil)
	require.NoError(t, err)
	internalHash, err := internal.managedAppDBCABundleHash(ctx, manager)
	require.NoError(t, err)
	assert.Equal(t, "bundle-hash", internalHash)

	caFilePath, err := internal.appDBCAFilePath(ctx, manager)
	require.NoError(t, err)
	assert.Equal(t, path.Join(util.ConfigMapVolumeCAMountPath, "bundle-hash"), caFilePath,
		"the AppDB must read the bundle through its hash-keyed file so a rotation takes effect")

	externalDB := validExternalAppDBMongoDBMultiClusterWithTLS(false, "")
	externalDB.Name = manager.Spec.AppDB.Name()
	externalDB.Namespace = manager.Namespace
	externalDB.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	require.NoError(t, central.Create(ctx, externalDB))
	externalOm := withExternalAppDBRef(DefaultOpsManagerBuilder().Build(), &omv1.ExternalAppDBRef{
		Name: externalDB.Name,
		Kind: omv1.ExternalAppDBRefKindMongoDBMultiCluster,
	})
	externalOm.Namespace = manager.Namespace
	external := r.createNewExternalAppDBReconciler(zap.S())
	refObject, err := external.fetchExternalAppDBRefObject(ctx, externalOm.Spec.ExternalAppDBRef)
	require.NoError(t, err)
	externalHash, err := external.managedAppDBCABundleHash(ctx, externalOm, refObject)
	require.NoError(t, err)
	assert.Equal(t, internalHash, externalHash, "both reconcilers must report the same hash for the same AppDB")
}
