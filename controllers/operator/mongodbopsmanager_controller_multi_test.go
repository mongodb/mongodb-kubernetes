package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
)

func omStsName(name string, clusterIdx int) string {
	return fmt.Sprintf("%s-%d", name, clusterIdx)
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
		SetName(opsManager.Spec.GetOpsManagerCA()).
		SetNamespace(opsManager.GetNamespace()).
		SetDataField("mms-ca.crt", string(cert)).
		Build()

	err := kubeClient.Create(ctx, &cm)
	require.NoError(t, err)

	return opsManager.Spec.GetOpsManagerCA()
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
		assert.True(c.t, res.Requeue || res.RequeueAfter > 0, "result=%+v", res)
	} else {
		assert.True(c.t, !res.Requeue && res.RequeueAfter > 0)
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
	appDB := opsManager.Spec.AppDB

	reconciler, omClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, memberClusterMap, omConnectionFactory)

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
	centralClusterChecks.checkOMCAConfigMap(opsManager.Spec.GetOpsManagerCA())

	for clusterIdx, clusterSpecItem := range clusterSpecItems {
		memberClusterClient := memberClusterMap[clusterSpecItem.ClusterName]
		memberClusterChecks := newOMMemberClusterChecks(ctx, t, opsManager, clusterSpecItem.ClusterName, memberClusterClient, clusterIdx)
		memberClusterChecks.checkStatefulSetExists()
		memberClusterChecks.checkGenKeySecret(opsManager.Name)
		memberClusterChecks.checkConnectionStringSecret(opsManager.Name)
		memberClusterChecks.checkPEMSecret(appDbPemSecretName, appDbTLSSecretPemHash)
		memberClusterChecks.checkPEMSecret(omPemSecretName, omTLSSecretPemHash)
		memberClusterChecks.checkAppDBCAConfigMap(appDbCAConfigMapName)
		memberClusterChecks.checkOMCAConfigMap(opsManager.Spec.GetOpsManagerCA())
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
	appDB := opsManager.Spec.AppDB

	reconciler, omClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", opsManager, memberClusterMap, omConnectionFactory)

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
	centralClusterChecks.checkOMCAConfigMap(opsManager.Spec.GetOpsManagerCA())

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
		memberClusterChecks.checkOMCAConfigMap(opsManager.Spec.GetOpsManagerCA())
	}
}
