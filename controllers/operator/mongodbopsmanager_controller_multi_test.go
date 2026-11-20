package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func newMultiClusterManagedAppDB(t *testing.T) (*omv1.MongoDBOpsManager, kubernetesClient.Client, *OpsManagerReconciler) {
	t.Helper()
	manager := newAppDBManagedCertificateTestOM()
	manager.Spec.AppDB.Topology = omv1.ClusterTopologyMultiCluster
	manager.Spec.AppDB.Members = 0
	eastDomain, westDomain := "east.example.com", "west.example.com"
	manager.Spec.AppDB.ClusterSpecList = mdbv1.ClusterSpecList{
		{ClusterName: "east", Members: 2, ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: &eastDomain}},
		{ClusterName: "west", Members: 5, ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: &westDomain}},
	}
	c := newManagedCertificateKubeClient(t, manager,
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: manager.Spec.AppDB.ClusterMappingConfigMapName(), Namespace: manager.Namespace},
			Data:       map[string]string{"east": "4", "west": "7", "retired": "9"},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: manager.Spec.AppDB.LastAppliedMemberSpecConfigMapName(), Namespace: manager.Namespace},
			Data:       map[string]string{"east": "3", "west": "1", "retired": "1"},
		})
	r := newManagedCertificateTestReconciler(t.Context(), c)
	r.memberClustersMap = map[string]client.Client{"east": newManagedCertificateKubeClient(t), "west": newManagedCertificateKubeClient(t)}
	r.certManagerReadiness = certs.CertManagerReadiness{Ready: true}
	return manager, c, r
}

func TestAppDBManagedCertificatesMultiClusterCoverage(t *testing.T) {
	ctx := t.Context()
	manager, c, r := newMultiClusterManagedAppDB(t)
	delete(r.memberClustersMap, "west")

	result := r.ensureAppDBCertAndCA(ctx, manager, zap.S())
	require.NotNil(t, result.condition, "managed multi-cluster AppDB must participate in certificate preparation")
	require.False(t, result.status.IsOK(), "initial issuance must wait for cert-manager")
	leaf := getManagedCertificateObject(t, ctx, c, "managed-om-db-managed-cert")
	assert.Contains(t, leaf.Spec.Usages, certmanagerv1.UsageServerAuth)
	assert.Contains(t, leaf.Spec.Usages, certmanagerv1.UsageClientAuth, "SCRAM members use this certificate for peer TLS client connections")
	assert.Contains(t, leaf.Spec.DNSNames, "managed-om-db-4-2-svc.my-namespace.svc.cluster.local", "retain currently running members during a scale-down")
	assert.Contains(t, leaf.Spec.DNSNames, "managed-om-db-7-4-svc.my-namespace.svc.cluster.local", "include the full target of an unavailable cluster even while another cluster scales")
	assert.Contains(t, leaf.Spec.DNSNames, "managed-om-db-4-2.east.example.com")
	assert.Contains(t, leaf.Spec.DNSNames, "managed-om-db-7-4.west.example.com")
	assert.Contains(t, leaf.Spec.DNSNames, "managed-om-db-9-0-svc.my-namespace.svc.cluster.local", "retain removed clusters until their members have scaled down")
	assert.NotContains(t, leaf.Spec.DNSNames, "managed-om-db-0.managed-om-db-svc.my-namespace.svc.cluster.local")
	require.Len(t, leaf.OwnerReferences, 1)
	assert.Equal(t, "managed-om", leaf.OwnerReferences[0].Name)
	assert.Equal(t, manager.UID, leaf.OwnerReferences[0].UID)
	coverage := map[string]int{}
	require.NoError(t, json.Unmarshal([]byte(leaf.Annotations["mongodb.com/covered-members-per-cluster"]), &coverage))
	assert.Equal(t, map[string]int{"east": 3, "west": 5, "retired": 1}, coverage)
	before := leaf.Spec.DeepCopy()

	manager.Spec.AppDB.ClusterSpecList[0], manager.Spec.AppDB.ClusterSpecList[1] = manager.Spec.AppDB.ClusterSpecList[1], manager.Spec.AppDB.ClusterSpecList[0]
	manager.Spec.AppDB.ClusterSpecList[0].Members = 1
	r.ensureAppDBCertAndCA(ctx, manager, zap.S())
	leaf = getManagedCertificateObject(t, ctx, c, "managed-om-db-managed-cert")
	assert.Equal(t, *before, leaf.Spec, "reordering and scaling down must not rename or shrink the shared certificate")
	assert.Empty(t, listManagedCertificateObjects(t, ctx, kubernetesClient.NewClient(r.memberClustersMap["east"])), "issuance stays central")
}

func TestAppDBManagedCertificatesMultiClusterOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		issue  bool
	}{
		{name: "owned member permits issuance", labels: map[string]string{util.MongoDBOpsManagerResourceOwnerLabel: "managed-om"}, issue: true},
		{name: "foreign member defers issuance", labels: map[string]string{util.MongoDBResourceOwnerLabel: "other"}},
		{name: "ownerless member defers issuance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, c, r := newMultiClusterManagedAppDB(t)
			member := r.memberClustersMap["west"]
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "managed-om-db-7", Namespace: manager.Namespace, Labels: tc.labels}}
			require.NoError(t, member.Create(t.Context(), sts))
			result := r.ensureAppDBCertAndCA(t.Context(), manager, zap.S())
			if tc.issue {
				require.NotNil(t, result.condition)
				assert.Contains(t, managedCertificateNames(listManagedCertificateObjects(t, t.Context(), c)), "managed-om-db-managed-cert")
			} else {
				assert.Nil(t, result.condition)
				assert.True(t, result.status.IsOK())
				assert.Empty(t, listManagedCertificateObjects(t, t.Context(), c))
			}
			persisted := &appsv1.StatefulSet{}
			require.NoError(t, member.Get(t.Context(), kube.ObjectKey(manager.Namespace, sts.Name), persisted))
			assert.Equal(t, sts.ObjectMeta, persisted.ObjectMeta, "preparation must not change member ownership")
		})
	}

	t.Run("member read error fails before issuance", func(t *testing.T) {
		manager, c, r := newMultiClusterManagedAppDB(t)
		r.memberClustersMap["west"] = newManagedCertificateKubeClientWithInterceptor(t, interceptor.Funcs{
			Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*appsv1.StatefulSet); ok {
					return fmt.Errorf("west StatefulSet read denied")
				}
				return inner.Get(ctx, key, obj, opts...)
			},
		})
		result := r.ensureAppDBCertAndCA(t.Context(), manager, zap.S())
		require.NotNil(t, result.condition)
		assert.Equal(t, "IssuanceFailed", result.condition.Reason)
		assert.Contains(t, result.condition.Message, "west StatefulSet read denied")
		assert.Empty(t, listManagedCertificateObjects(t, t.Context(), c))
	})
}

func TestOpsManagerManagedMultiClusterTrustCopies(t *testing.T) {
	ctx := t.Context()
	manager, central, r := newMultiClusterManagedAppDB(t)
	manager.Spec.Topology = omv1.ClusterTopologyMultiCluster
	manager.Spec.ClusterSpecList = []omv1.ClusterSpecOMItem{{ClusterName: "east", Members: 1}, {ClusterName: "west", Members: 1}}
	manager.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
	r.memberClustersMap["east"] = central
	helper := newManagedCertificateTestHelper(t, r, manager)

	for _, tc := range []struct {
		name           string
		key            string
		copy           func() error
		writtenByAppDB bool
	}{
		{name: "managed-om-managed-om-ca-bundle", key: "mms-ca.crt", copy: func() error {
			return r.replicateTLSCAInMemberClusters(ctx, helper)
		}},
		{name: "managed-om-db-managed-server-ca-bundle", key: "ca-pem", writtenByAppDB: true, copy: func() error {
			return r.replicateAppDBTLSCAInMemberClusters(ctx, helper, &AppDBConfig{CAConfigMapName: "managed-om-db-managed-server-ca-bundle"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: tc.name, Namespace: manager.Namespace, UID: types.UID(tc.name), OwnerReferences: kube.BaseOwnerReference(manager),
			}, Data: map[string]string{tc.key: "issued trust"}}
			require.NoError(t, central.Create(ctx, source))
			require.NoError(t, tc.copy())
			retained, err := central.GetConfigMap(ctx, kube.ObjectKey(manager.Namespace, tc.name))
			require.NoError(t, err)
			assert.Equal(t, source.OwnerReferences, retained.OwnerReferences)
			assert.Equal(t, source.ResourceVersion, retained.ResourceVersion, "replication must not rewrite the shared central source")
			remote := &corev1.ConfigMap{}
			err = r.memberClustersMap["west"].Get(ctx, kube.ObjectKey(manager.Namespace, tc.name), remote)
			if tc.writtenByAppDB {
				assert.True(t, apiErrors.IsNotFound(err), "the AppDB reconciler replicates the managed AppDB bundle; Ops Manager must not write it")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, source.Data, remote.Data)
			assert.Equal(t, manager.GetOwnerLabels(), remote.Labels)
			assert.Empty(t, remote.OwnerReferences)
		})
	}
}

// TestOpsManagerLeavesManagedAppDBCABundleToTheAppDB checks that Ops Manager does not touch the
// AppDB-owned managed CA bundle in a member cluster, where the database keeps it current during a
// rotation and removes it on deletion.
func TestOpsManagerLeavesManagedAppDBCABundleToTheAppDB(t *testing.T) {
	ctx := t.Context()
	manager, central, r := newMultiClusterManagedAppDB(t)
	r.memberClustersMap["east"] = central
	helper := newManagedCertificateTestHelper(t, r, manager)

	const bundleName = "managed-om-db-managed-server-ca-bundle"
	require.NoError(t, central.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: bundleName, Namespace: manager.Namespace, UID: types.UID("central-bundle")},
		Data:       map[string]string{"ca-pem": "current"},
	}))
	appDBOwned := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: bundleName, Namespace: manager.Namespace,
			Labels: map[string]string{util.ManagedCertificateOwnerUIDLabel: "appdb-uid", util.OperatorLabelName: util.OperatorLabelValue},
		},
		Data: map[string]string{"ca-pem": "stale"},
	}
	require.NoError(t, r.memberClustersMap["west"].Create(ctx, appDBOwned))

	require.NoError(t, r.replicateAppDBTLSCAInMemberClusters(ctx, helper, &AppDBConfig{CAConfigMapName: bundleName}))

	persisted := &corev1.ConfigMap{}
	require.NoError(t, r.memberClustersMap["west"].Get(ctx, kube.ObjectKey(manager.Namespace, bundleName), persisted))
	assert.Equal(t, appDBOwned.ObjectMeta, persisted.ObjectMeta)
	assert.Equal(t, appDBOwned.Data, persisted.Data, "the database owns this bundle; Ops Manager must not revert a CA rotation")
}

func omStsName(name string, clusterIdx int) string {
	return fmt.Sprintf("%s-%d", name, clusterIdx)
}

func backupDaemonStsName(name string, clusterIdx int) string {
	return fmt.Sprintf("%s-%d-backup-daemon", name, clusterIdx)
}

func genKeySecretName(omName string) string {
	return fmt.Sprintf("%s-gen-key", omName)
}

func connectionStringSecretName(omName string) string {
	return fmt.Sprintf("%s-db-connection-string", omName)
}

func agentPasswordSecretName(omName string) string {
	return fmt.Sprintf("%s-db-agent-password", omName)
}

func omPasswordSecretName(omName string) string {
	return fmt.Sprintf("%s-db-om-password", omName)
}

func omUserScramCredentialsSecretName(omName string) string {
	return fmt.Sprintf("%s-db-om-user-scram-credentials", omName)
}

type omMemberClusterChecks struct {
	ctx          context.Context
	t            *testing.T
	namespace    string
	clusterName  string
	kubeClient   client.Client
	clusterIndex int
	om           *omv1.MongoDBOpsManager
}

func newOMMemberClusterChecks(ctx context.Context, t *testing.T, opsManager *omv1.MongoDBOpsManager, clusterName string, kubeClient client.Client, clusterIndex int) *omMemberClusterChecks {
	result := omMemberClusterChecks{
		ctx:          ctx,
		t:            t,
		namespace:    opsManager.Namespace,
		om:           opsManager,
		clusterName:  clusterName,
		kubeClient:   kubeClient,
		clusterIndex: clusterIndex,
	}

	return &result
}

func createOMCAConfigMap(ctx context.Context, t *testing.T, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager) string {
	cert, _ := createMockCertAndKeyBytes()
	cm := configmap.Builder().
		SetName(opsManager.GetOpsManagerCA()).
		SetNamespace(opsManager.GetNamespace()).
		SetDataField("mms-ca.crt", string(cert)).
		Build()

	err := kubeClient.Create(ctx, &cm)
	require.NoError(t, err)

	return opsManager.GetOpsManagerCA()
}

func createOMTLSCert(ctx context.Context, t *testing.T, kubeClient client.Client, opsManager *omv1.MongoDBOpsManager) (string, string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opsManager.TLSCertificateSecretName(),
			Namespace: opsManager.GetNamespace(),
		},
		Type: corev1.SecretTypeTLS,
	}

	certs := map[string][]byte{}
	certs["tls.crt"], certs["tls.key"] = createMockCertAndKeyBytes()

	secret.Data = certs
	err := kubeClient.Create(ctx, secret)
	require.NoError(t, err)

	pemHash := enterprisepem.ReadHashFromData(secrets.DataToStringData(secret.Data), zap.S())
	require.NotEmpty(t, pemHash)

	return secret.Name, pemHash
}

func (c *omMemberClusterChecks) checkStatefulSetExists() {
	sts := appsv1.StatefulSet{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.om.Namespace, omStsName(c.om.Name, c.clusterIndex)), &sts)
	assert.NoError(c.t, err)
}

func (c *omMemberClusterChecks) checkSecretNotFound(secretName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, secretName), &sec)
	assert.Error(c.t, err, "clusterName: %s", c.clusterName)
	assert.True(c.t, apiErrors.IsNotFound(err))
}

func (c *omMemberClusterChecks) checkGenKeySecret(omName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, genKeySecretName(omName)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "gen.key", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkClusterMapping(omName string, expectedClusterMapping map[string]int) {
	checkClusterMapping(c.ctx, c.t, c.kubeClient, c.namespace, omName, expectedClusterMapping)
	checkLegacyClusterMapping(c.ctx, c.t, c.kubeClient, c.namespace, omName, expectedClusterMapping)
}

func (c *omMemberClusterChecks) checkConnectionStringSecret(omName string) {
	sec := corev1.Secret{}
	secretName := connectionStringSecretName(omName)
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, secretName), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "connectionString", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkAgentPasswordSecret(omName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, agentPasswordSecretName(omName)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "password", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkOmPasswordSecret(omName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, omPasswordSecretName(omName)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "password", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkPEMSecret(secretName string, pemHash string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, secretName), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, sec.Data, pemHash, "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkAppDBCAConfigMap(configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, cm.Data, "ca-pem", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkOMCAConfigMap(configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, cm.Data, "mms-ca.crt", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) checkOmUserScramCredentialsSecretName(omName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(c.ctx, kube.ObjectKey(c.namespace, omUserScramCredentialsSecretName(omName)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "sha-1-server-key", "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "sha-1-stored-key", "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "sha-256-server-key", "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "sha-256-stored-key", "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "sha1-salt", "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, "sha256-salt", "clusterName: %s", c.clusterName)
}

func (c *omMemberClusterChecks) reconcileAndCheck(reconciler reconcile.Reconciler, expectedRequeue bool) {
	res, err := reconciler.Reconcile(c.ctx, requestFromObject(c.om))
	if expectedRequeue {
		assert.True(c.t, res.RequeueAfter > 0, "result=%+v", res)
	} else {
		assert.True(c.t, res.RequeueAfter > 0)
	}
	assert.NoError(c.t, err)
}

func TestOpsManagerMultiCluster(t *testing.T) {
	ctx := context.Background()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "kind-e2e-cluster-1"
	memberClusterName2 := "kind-e2e-cluster-2"
	clusters := []string{memberClusterName, memberClusterName2}
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters, omConnectionFactory)

	appDBClusterSpecItems := mdbv1.ClusterSpecList{
		{
			ClusterName: memberClusterName,
			Members:     1,
		},
		{
			ClusterName: memberClusterName2,
			Members:     2,
		},
	}
	clusterSpecItems := []omv1.ClusterSpecOMItem{
		{
			ClusterName: memberClusterName,
			Members:     1,
			Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{
				Members: 1,
			},
		},
		{
			ClusterName: memberClusterName2,
			Members:     1,
		},
	}

	builder := DefaultOpsManagerBuilder().
		SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
		SetOpsManagerClusterSpecList(clusterSpecItems).
		SetTLSConfig(omv1.MongoDBOpsManagerTLS{
			CA: "om-ca",
		}).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDbMembers(0).
		SetAppDBClusterSpecList(appDBClusterSpecItems).
		SetAppDBTLSConfig(mdbv1.TLSConfig{
			Enabled:                      true,
			AdditionalCertificateDomains: nil,
			CA:                           "appdb-ca",
		})

	opsManager := builder.Build()
	opsManager.Spec.Security.CertificatesSecretsPrefix = "om-prefix"
	appDB := *opsManager.Spec.AppDB

	reconciler, omClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, memberClusterMap, omConnectionFactory, architectures.NonStatic)

	// prepare TLS certificates and CA in central cluster

	appDbCAConfigMapName := createAppDbCAConfigMap(ctx, t, omClient, appDB)
	appDbTLSCertSecret, appDbTLSSecretPemHash := createAppDBTLSCert(ctx, t, omClient, appDB)
	appDbPemSecretName := appDbTLSCertSecret + "-pem"

	/* omCAConfigMapName */
	_ = createOMCAConfigMap(ctx, t, omClient, opsManager)
	omTLSCertSecret, omTLSSecretPemHash := createOMTLSCert(ctx, t, omClient, opsManager)
	omPemSecretName := omTLSCertSecret + "-pem"

	/* 	checkOMReconciliationSuccessful(t, reconciler, opsManager) */

	centralClusterChecks := newOMMemberClusterChecks(ctx, t, opsManager, centralClusterName, omClient, -1)
	centralClusterChecks.reconcileAndCheck(reconciler, true)
	// secrets and config maps created in the central cluster
	centralClusterChecks.checkClusterMapping(opsManager.Name, map[string]int{
		memberClusterName:  0,
		memberClusterName2: 1,
	})
	centralClusterChecks.checkGenKeySecret(opsManager.Name)
	centralClusterChecks.checkAgentPasswordSecret(opsManager.Name)
	centralClusterChecks.checkOmPasswordSecret(opsManager.Name)
	centralClusterChecks.checkOmUserScramCredentialsSecretName(opsManager.Name)
	centralClusterChecks.checkSecretNotFound(appDbPemSecretName)
	centralClusterChecks.checkSecretNotFound(omPemSecretName)
	centralClusterChecks.checkOMCAConfigMap(opsManager.GetOpsManagerCA())

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]
		memberClusterChecks := newOMMemberClusterChecks(ctx, t, opsManager, clusterSpecItem.ClusterName, memberClusterClient, clusterIdx)
		memberClusterChecks.checkStatefulSetExists()
		memberClusterChecks.checkGenKeySecret(opsManager.Name)
		memberClusterChecks.checkConnectionStringSecret(opsManager.Name)
		memberClusterChecks.checkPEMSecret(appDbPemSecretName, appDbTLSSecretPemHash)
		memberClusterChecks.checkPEMSecret(omPemSecretName, omTLSSecretPemHash)
		memberClusterChecks.checkAppDBCAConfigMap(appDbCAConfigMapName)
		memberClusterChecks.checkOMCAConfigMap(opsManager.GetOpsManagerCA())
	}
}

// TestOpsManagerMultiClusterWithKMIP verifies that in multi-cluster topology the KMIP configuration is
// read from the central cluster (where MongoDB resources live) and applied to the Ops Manager and
// Backup Daemon statefulsets created in member clusters.
// All KMIP-related resources are created only in the central cluster's fake client. Member clusters use
// separate fake clients that don't contain any MongoDB resources, simulating a setup where member clusters
// don't have access to MongoDB CRs.
func TestOpsManagerMultiClusterWithKMIP(t *testing.T) {
	ctx := context.Background()

	kmipURL := "kmip.mongodb.com:5696"
	kmipCAConfigMapName := "kmip-ca"
	mdbName := "test-mdb"

	clientCertificatePrefix := "test-prefix"
	expectedClientCertificateSecretName := clientCertificatePrefix + "-" + mdbName + "-kmip-client"

	memberClusterName := "kind-e2e-cluster-1"
	memberClusterName2 := "kind-e2e-cluster-2"
	clusters := []string{memberClusterName, memberClusterName2}
	appDBClusterSpecItems := mdbv1.ClusterSpecList{
		{
			ClusterName: memberClusterName,
			Members:     1,
		},
		{
			ClusterName: memberClusterName2,
			Members:     2,
		},
	}
	clusterSpecItems := []omv1.ClusterSpecOMItem{
		{
			ClusterName: memberClusterName,
			Members:     1,
			Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{
				Members: 1,
			},
		},
		{
			ClusterName: memberClusterName2,
			Members:     1,
			Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{
				Members: 1,
			},
		},
	}

	testOm := DefaultOpsManagerBuilder().
		SetOpsManagerTopology(mdbv1.ClusterTopologyMultiCluster).
		SetOpsManagerClusterSpecList(clusterSpecItems).
		SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster).
		SetAppDBClusterSpecList(appDBClusterSpecItems).
		AddOplogStoreConfig("oplog-store-2", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		AddBlockStoreConfig("block-store-config-0", "my-user", types.NamespacedName{Name: "config-0-mdb", Namespace: mock.TestNamespace}).
		Build()

	testOm.Spec.Backup.Encryption = &omv1.Encryption{
		Kmip: &omv1.KmipConfig{
			Server: v1.KmipServerConfig{
				CA:  kmipCAConfigMapName,
				URL: kmipURL,
			},
		},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters, omConnectionFactory)
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, memberClusterMap, omConnectionFactory, architectures.NonStatic)
	addKMIPTestResources(ctx, client, testOm, mdbName, clientCertificatePrefix)
	configureBackupResources(ctx, client, testOm)

	checkOMReconciliationSuccessful(ctx, t, reconciler, testOm, reconciler.client)

	host, port, _ := net.SplitHostPort(kmipURL)

	expectedVars := []corev1.EnvVar{
		{Name: "OM_PROP_backup_kmip_server_host", Value: host},
		{Name: "OM_PROP_backup_kmip_server_port", Value: port},
		{Name: "OM_PROP_backup_kmip_server_ca_file", Value: util.KMIPCAFileInContainer},
	}
	expectedCAMount := corev1.VolumeMount{
		Name:      util.KMIPServerCAName,
		MountPath: util.KMIPServerCAHome,
		ReadOnly:  true,
	}
	expectedClientCertMount := corev1.VolumeMount{
		Name:      util.KMIPClientSecretNamePrefix + expectedClientCertificateSecretName,
		MountPath: util.KMIPClientSecretsHome + "/" + expectedClientCertificateSecretName,
		ReadOnly:  true,
	}
	expectedCAVolume := statefulset.CreateVolumeFromConfigMap(util.KMIPServerCAName, kmipCAConfigMapName)
	expectedClientCertVolume := statefulset.CreateVolumeFromSecret(util.KMIPClientSecretNamePrefix+expectedClientCertificateSecretName, expectedClientCertificateSecretName)

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]

		for _, stsName := range []string{omStsName(testOm.Name, clusterIdx), backupDaemonStsName(testOm.Name, clusterIdx)} {
			sts := appsv1.StatefulSet{}
			err := memberClusterClient.Get(ctx, kube.ObjectKey(testOm.Namespace, stsName), &sts)
			require.NoError(t, err, "statefulset %s should exist in cluster %s", stsName, clusterSpecItem.ClusterName)

			envs := sts.Spec.Template.Spec.Containers[0].Env
			volumes := sts.Spec.Template.Spec.Volumes
			volumeMounts := sts.Spec.Template.Spec.Containers[0].VolumeMounts

			assert.Subset(t, envs, expectedVars, "statefulset %s in cluster %s", stsName, clusterSpecItem.ClusterName)
			assert.Contains(t, volumeMounts, expectedCAMount, "statefulset %s in cluster %s", stsName, clusterSpecItem.ClusterName)
			assert.Contains(t, volumeMounts, expectedClientCertMount, "statefulset %s in cluster %s", stsName, clusterSpecItem.ClusterName)
			assert.Contains(t, volumes, expectedCAVolume, "statefulset %s in cluster %s", stsName, clusterSpecItem.ClusterName)
			assert.Contains(t, volumes, expectedClientCertVolume, "statefulset %s in cluster %s", stsName, clusterSpecItem.ClusterName)
		}
	}
}

func TestOpsManagerMultiClusterUnreachableNoPanic(t *testing.T) {
	ctx := context.Background()
	centralClusterName := multicluster.LegacyCentralClusterName
	memberClusterName := "kind-e2e-cluster-1"
	memberClusterName2 := "kind-e2e-cluster-2"
	memberClusterNameUnreachable := "kind-e2e-cluster-unreachable"
	clusters := []string{memberClusterName, memberClusterName2}
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	memberClusterMap := getFakeMultiClusterMapWithClusters(clusters, omConnectionFactory)

	appDBClusterSpecItems := []mdbv1.ClusterSpecItem{
		{
			ClusterName: memberClusterName,
			Members:     1,
		},
		{
			ClusterName: memberClusterName2,
			Members:     2,
		},
	}
	clusterSpecItems := []omv1.ClusterSpecOMItem{
		{
			ClusterName: memberClusterName,
			Members:     1,
			Backup: &omv1.MongoDBOpsManagerBackupClusterSpecItem{
				Members: 1,
			},
		},
		{
			ClusterName: memberClusterName2,
			Members:     1,
		},
		{
			ClusterName: memberClusterNameUnreachable,
			Members:     1,
		},
	}

	builder := DefaultOpsManagerBuilder().
		SetOpsManagerTopology(omv1.ClusterTopologyMultiCluster).
		SetOpsManagerClusterSpecList(clusterSpecItems).
		SetTLSConfig(omv1.MongoDBOpsManagerTLS{
			CA: "om-ca",
		}).
		SetAppDBTopology(omv1.ClusterTopologyMultiCluster).
		SetAppDbMembers(0).
		SetAppDBClusterSpecList(appDBClusterSpecItems).
		SetAppDBTLSConfig(mdbv1.TLSConfig{
			Enabled:                      true,
			AdditionalCertificateDomains: nil,
			CA:                           "appdb-ca",
		})

	opsManager := builder.Build()
	opsManager.Spec.Security.CertificatesSecretsPrefix = "om-prefix"
	appDB := *opsManager.Spec.AppDB

	reconciler, omClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, memberClusterMap, omConnectionFactory, architectures.NonStatic)

	// prepare TLS certificates and CA in central cluster

	appDbCAConfigMapName := createAppDbCAConfigMap(ctx, t, omClient, appDB)
	appDbTLSCertSecret, appDbTLSSecretPemHash := createAppDBTLSCert(ctx, t, omClient, appDB)
	appDbPemSecretName := appDbTLSCertSecret + "-pem"

	/* omCAConfigMapName */
	_ = createOMCAConfigMap(ctx, t, omClient, opsManager)
	omTLSCertSecret, omTLSSecretPemHash := createOMTLSCert(ctx, t, omClient, opsManager)
	omPemSecretName := omTLSCertSecret + "-pem"

	/* 	checkOMReconciliationSuccessful(t, reconciler, opsManager) */

	centralClusterChecks := newOMMemberClusterChecks(ctx, t, opsManager, centralClusterName, omClient, -1)
	require.NotPanics(t, func() {
		centralClusterChecks.reconcileAndCheck(reconciler, true)
	})

	// secrets and config maps created in the central cluster
	centralClusterChecks.checkGenKeySecret(opsManager.Name)
	centralClusterChecks.checkAgentPasswordSecret(opsManager.Name)
	centralClusterChecks.checkOmPasswordSecret(opsManager.Name)
	centralClusterChecks.checkOmUserScramCredentialsSecretName(opsManager.Name)
	centralClusterChecks.checkSecretNotFound(appDbPemSecretName)
	centralClusterChecks.checkSecretNotFound(omPemSecretName)
	centralClusterChecks.checkOMCAConfigMap(opsManager.GetOpsManagerCA())

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		if clusterSpecItem.ClusterName == memberClusterNameUnreachable {
			continue
		}

		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]
		memberClusterChecks := newOMMemberClusterChecks(ctx, t, opsManager, clusterSpecItem.ClusterName, memberClusterClient, clusterIdx)
		memberClusterChecks.checkStatefulSetExists()
		memberClusterChecks.checkGenKeySecret(opsManager.Name)
		memberClusterChecks.checkConnectionStringSecret(opsManager.Name)
		memberClusterChecks.checkPEMSecret(appDbPemSecretName, appDbTLSSecretPemHash)
		memberClusterChecks.checkPEMSecret(omPemSecretName, omTLSSecretPemHash)
		memberClusterChecks.checkAppDBCAConfigMap(appDbCAConfigMapName)
		memberClusterChecks.checkOMCAConfigMap(opsManager.GetOpsManagerCA())
	}
}
