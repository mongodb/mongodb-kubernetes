package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type clusterChecks struct {
	t            *testing.T
	namespace    string
	clusterName  string
	clusterIndex int
	kubeClient   client.Client
}

func newClusterChecks(t *testing.T, clusterName string, clusterIndex int, namespace string, kubeClient client.Client) *clusterChecks {
	result := clusterChecks{
		t:            t,
		namespace:    namespace,
		clusterName:  clusterName,
		kubeClient:   kubeClient,
		clusterIndex: clusterIndex,
	}

	return &result
}

func (c *clusterChecks) checkAutomationConfigSecret(ctx context.Context, secretName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, secretName), &sec)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, sec.Data, automationconfig.ConfigKey, "clusterName: %s", c.clusterName)
}

func (c *clusterChecks) checkAgentAPIKeySecret(ctx context.Context, projectID string) string {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, agentAPIKeySecretName(projectID)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, util.OmAgentApiKey, "clusterName: %s", c.clusterName)
	return string(sec.Data[util.OmAgentApiKey])
}

func (c *clusterChecks) checkSecretNotFound(ctx context.Context, secretName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, secretName), &sec)
	assert.Error(c.t, err, "clusterName: %s", c.clusterName)
	assert.True(c.t, apiErrors.IsNotFound(err))
}

func (c *clusterChecks) checkConfigMapNotFound(ctx context.Context, configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.Error(c.t, err, "clusterName: %s", c.clusterName)
	assert.True(c.t, apiErrors.IsNotFound(err))
}

func (c *clusterChecks) checkPEMSecret(ctx context.Context, secretName string, pemHash string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, secretName), &sec)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, sec.Data, pemHash, "clusterName: %s", c.clusterName)
}

func (c *clusterChecks) checkAutomationConfigConfigMap(ctx context.Context, configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, cm.Data, appDBACConfigMapVersionField, "clusterName: %s", c.clusterName)
}

func (c *clusterChecks) checkTLSCAConfigMap(ctx context.Context, configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, cm.Data, "ca-pem", "clusterName: %s", c.clusterName)
}

func (c *clusterChecks) checkProjectIDConfigMap(ctx context.Context, configMapName string) string {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, cm.Data, util.AppDbProjectIdKey, "clusterName: %s", c.clusterName)
	return cm.Data[util.AppDbProjectIdKey]
}

func (c *clusterChecks) checkServices(ctx context.Context, statefulSetName string, expectedMembers int) {
	for podIdx := 0; podIdx < expectedMembers; podIdx++ {
		svc := corev1.Service{}
		serviceName := fmt.Sprintf("%s-%d-svc", statefulSetName, podIdx)
		err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, serviceName), &svc)
		require.NoError(c.t, err, "clusterName: %s", c.clusterName)

		assert.Equal(c.t, map[string]string{
			"controller":                         "mongodb-enterprise-operator",
			"statefulset.kubernetes.io/pod-name": fmt.Sprintf("%s-%d", statefulSetName, podIdx),
		},
			svc.Spec.Selector)
	}
}

func (c *clusterChecks) checkStatefulSet(ctx context.Context, statefulSetName string, expectedMembers int) {
	sts := appsv1.StatefulSet{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, statefulSetName), &sts)
	require.NoError(c.t, err, "clusterName: %s stsName: %s", c.clusterName, statefulSetName)
	require.Equal(c.t, expectedMembers, int(*sts.Spec.Replicas))
	require.Equal(c.t, statefulSetName, sts.ObjectMeta.Name)
}

func (c *clusterChecks) checkStatefulSetDoesNotExist(ctx context.Context, statefulSetName string) {
	sts := appsv1.StatefulSet{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, statefulSetName), &sts)
	require.True(c.t, apiErrors.IsNotFound(err))
}

func (c *clusterChecks) checkAgentCertsSecret(ctx context.Context, certificatesSecretsPrefix string, resourceName string) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, fmt.Sprintf("%s-%s-%s-pem", certificatesSecretsPrefix, resourceName, util.AgentSecretName)), &sec)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Contains(c.t, sec.Data, util.AutomationAgentPemSecretKey, "clusterName: %s", c.clusterName)
}

func (c *clusterChecks) checkMongosCertsSecret(ctx context.Context, certificatesSecretsPrefix string, resourceName string, shouldExist bool) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, fmt.Sprintf("%s-%s-%s-pem", certificatesSecretsPrefix, resourceName, "mongos-cert")), &sec)
	c.assertErrNotFound(err, shouldExist)
}

func (c *clusterChecks) checkConfigSrvCertsSecret(ctx context.Context, certificatesSecretsPrefix string, resourceName string, shouldExist bool) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, fmt.Sprintf("%s-%s-%s-pem", certificatesSecretsPrefix, resourceName, "config-cert")), &sec)
	c.assertErrNotFound(err, shouldExist)
}

func (c *clusterChecks) checkInternalClusterCertSecret(ctx context.Context, certificatesSecretsPrefix string, resourceName string, shardIdx int, shouldExist bool) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, fmt.Sprintf("%s-%s-%d-%s-pem", certificatesSecretsPrefix, resourceName, shardIdx, util.ClusterFileName)), &sec)
	c.assertErrNotFound(err, shouldExist)
}

func (c *clusterChecks) checkMemberCertSecret(ctx context.Context, certificatesSecretsPrefix string, resourceName string, shardIdx int, shouldExist bool) {
	sec := corev1.Secret{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, fmt.Sprintf("%s-%s-%d-cert-pem", certificatesSecretsPrefix, resourceName, shardIdx)), &sec)
	c.assertErrNotFound(err, shouldExist)
}

func (c *clusterChecks) assertErrNotFound(err error, shouldExist bool) {
	if shouldExist {
		require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	} else {
		require.Error(c.t, err, "clusterName: %s", c.clusterName)
		require.True(c.t, apiErrors.IsNotFound(err))
	}
}

func (c *clusterChecks) checkMMSCAConfigMap(ctx context.Context, configMapName string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	assert.NoError(c.t, err, "clusterName: %s", c.clusterName)
	assert.Contains(c.t, cm.Data, util.CaCertMMS, "clusterName: %s", c.clusterName)
}

func (c *clusterChecks) checkHostnameOverrideConfigMap(ctx context.Context, configMapName string, expectedPodNameToHostnameMap map[string]string) {
	cm := corev1.ConfigMap{}
	err := c.kubeClient.Get(ctx, kube.ObjectKey(c.namespace, configMapName), &cm)
	require.NoError(c.t, err, "clusterName: %s", c.clusterName)
	require.Equal(c.t, expectedPodNameToHostnameMap, cm.Data)
}
