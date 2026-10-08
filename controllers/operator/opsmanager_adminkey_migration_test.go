package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

var legacyAdminKeyData = map[string]string{
	util.OmPublicApiKey: "jane.doe@g.com",
	util.OmPrivateKey:   "jane.doe@g.com-key",
}

func TestMigrateLegacyAdminKeySecret_LegacyOnly(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData, nil)
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	status := reconciler.prepareOpsManagerAndAssertOK(ctx, t, testOm)
	assert.Equal(t, workflow.OK(), status)

	// No /unauth user creation must happen - the migration must not wipe out the existing key
	assert.Equal(t, 0, initializer.numberOfCalls, "user creation must not be triggered during migration")

	// Qualified secret exists with identical data and the OpsManagerNamespace label
	qualifiedKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name))
	data, err := secret.ReadStringData(ctx, client, qualifiedKey)
	require.NoError(t, err)
	assert.Equal(t, legacyAdminKeyData, data)

	qualifiedSecret, err := client.GetSecret(ctx, qualifiedKey)
	require.NoError(t, err)
	assert.Equal(t, testOm.Namespace, qualifiedSecret.Labels[omv1.OpsManagerNamespaceLabel])

	// Legacy secret is gone
	_, err = client.GetSecret(ctx, kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name)))
	assert.True(t, secret.SecretNotExist(err), "legacy secret should be deleted")

	// APIKeySecretName now resolves to the qualified name
	assertQualifiedAPIKeySecretName(ctx, t, client, testOm)
}

func TestMigrateLegacyAdminKeySecret_BothPresent(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), map[string]string{"stale": "data"}, nil)
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	qualifiedData := map[string]string{util.OmPublicApiKey: "qualified-pk", util.OmPrivateKey: "qualified-sk"}
	qualifiedName := fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name)
	qualifiedSecret := adminKeySecret(OperatorNamespace, qualifiedName, qualifiedData, map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
	require.NoError(t, reconciler.client.CreateSecret(ctx, qualifiedSecret))

	reconciler.prepareOpsManagerAndAssertOK(ctx, t, testOm)

	// Qualified data is untouched
	data, err := secret.ReadStringData(ctx, client, kube.ObjectKey(OperatorNamespace, qualifiedName))
	require.NoError(t, err)
	assert.Equal(t, qualifiedData, data)

	// Legacy secret is deleted
	_, err = client.GetSecret(ctx, kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name)))
	assert.True(t, secret.SecretNotExist(err), "legacy secret should be deleted")

	assert.Equal(t, 0, initializer.numberOfCalls)
}

func TestMigrateLegacyAdminKeySecret_QualifiedOnly(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	qualifiedData := map[string]string{util.OmPublicApiKey: "qualified-pk", util.OmPrivateKey: "qualified-sk"}
	qualifiedName := fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name)
	qualifiedSecret := adminKeySecret(OperatorNamespace, qualifiedName, qualifiedData, map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
	require.NoError(t, reconciler.client.CreateSecret(ctx, qualifiedSecret))

	reconciler.prepareOpsManagerAndAssertOK(ctx, t, testOm)

	// Nothing changed
	data, err := secret.ReadStringData(ctx, client, kube.ObjectKey(OperatorNamespace, qualifiedName))
	require.NoError(t, err)
	assert.Equal(t, qualifiedData, data)

	assert.Equal(t, 0, initializer.numberOfCalls)
	assert.Len(t, mock.GetMapForObject(client, &corev1.Secret{}), 2)
}

func TestMigrateLegacyAdminKeySecret_Idempotent(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData, nil)
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	reconciler.prepareOpsManagerAndAssertOK(ctx, t, testOm)
	qualifiedName := fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name)

	// Second call is a no-op: qualified exists, legacy is gone
	reconciler.prepareOpsManagerAndAssertOK(ctx, t, testOm)

	assert.Equal(t, 0, initializer.numberOfCalls)

	data, err := secret.ReadStringData(ctx, client, kube.ObjectKey(OperatorNamespace, qualifiedName))
	require.NoError(t, err)
	assert.Equal(t, legacyAdminKeyData, data)

	_, err = client.GetSecret(ctx, kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name)))
	assert.True(t, secret.SecretNotExist(err))
}

func TestMigrateLegacyAdminKeySecret_CrossNamespace(t *testing.T) {
	ctx := context.Background()

	// Victim OM in namespace A that goes through the legacy migration
	victimOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	require.NotEqual(t, OperatorNamespace, victimOm.Namespace)
	victimReconciler, _, _ := newMigrationTestReconciler(ctx, t, victimOm)

	legacySecret := adminKeySecret(OperatorNamespace, "shared-om-admin-key", legacyAdminKeyData, nil)
	require.NoError(t, victimReconciler.client.CreateSecret(ctx, legacySecret))

	victimReconciler.prepareOpsManagerAndAssertOK(ctx, t, victimOm)

	// A second OM with the SAME name in a different namespace
	attackerOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	attackerOm.Namespace = "other-namespace"
	require.Equal(t, victimOm.Name, attackerOm.Name)
	require.NotEqual(t, victimOm.Namespace, attackerOm.Namespace)

	// Its APIKeySecretName must NOT resolve to the victim's legacy secret (which is gone),
	// but to its own namespace-qualified name
	attackerClient, initializer := attackerReconcilerClient(ctx, t, attackerOm)
	assertQualifiedAPIKeySecretName(ctx, t, attackerClient, attackerOm)
	assert.Equal(t, 0, initializer.numberOfCalls)
}

// TestOpsManagerReconciler_prepareOpsManagerFreshInstall checks that a fresh install
// (no admin key secrets seeded) still behaves like the existing prepareOpsManager test:
// the admin user is created and two secrets exist.
func TestOpsManagerReconciler_prepareOpsManagerFreshInstall(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, client, initializer := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)

	reconcileStatus, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
	assert.Equal(t, workflow.OK(), reconcileStatus)

	assert.Equal(t, 1, initializer.numberOfCalls)
	assert.Len(t, initializer.currentUsers, 1)

	// One secret created by the user, another one - by the Operator for the user public key
	assert.Len(t, mock.GetMapForObject(client, &corev1.Secret{}), 2)
}

// --- helpers ---

func newMigrationTestReconciler(ctx context.Context, t *testing.T, testOm *omv1.MongoDBOpsManager) (*OpsManagerReconciler, kubernetesClient.Client, *MockedInitializer) {
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	return defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
}

// attackerReconcilerClient builds a reconciler for a second OM CR in a different namespace,
// sharing nothing but the fake client with the first one.
func attackerReconcilerClient(ctx context.Context, t *testing.T, attackerOm *omv1.MongoDBOpsManager) (kubernetesClient.Client, *MockedInitializer) {
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	_, client, initializer := defaultTestOmReconciler(ctx, t, nil, "", "", attackerOm, nil, omConnectionFactory, architectures.NonStatic)
	return client, initializer
}

func adminKeySecret(namespace, name string, data map[string]string, labels map[string]string) corev1.Secret {
	builder := secret.Builder().
		SetName(name).
		SetNamespace(namespace).
		SetStringMapToData(data)
	if labels != nil {
		builder = builder.SetLabels(labels)
	}
	return builder.Build()
}

func (r *OpsManagerReconciler) prepareOpsManagerAndAssertOK(ctx context.Context, t *testing.T, testOm *omv1.MongoDBOpsManager) workflow.Status {
	status, _ := r.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
	require.Equal(t, workflow.OK(), status)
	return status
}

func assertQualifiedAPIKeySecretName(ctx context.Context, t *testing.T, client kubernetesClient.Client, testOm *omv1.MongoDBOpsManager) {
	apiKeySecretName, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name), apiKeySecretName)
}
