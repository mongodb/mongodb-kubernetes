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

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
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

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), map[string]string{"stale": "data"},
		map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
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

// TestMigrateLegacyAdminKeySecret_UnlabeledLegacySecret checks that an unlabeled
// legacy Secret is not migrated, not copied and not deleted: the migration fails
// closed because ownership cannot be proven.
func TestMigrateLegacyAdminKeySecret_UnlabeledLegacySecret(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData, nil)
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	status, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
	assert.NotEqual(t, workflow.OK(), status, "migration must fail closed for an unlabeled legacy secret")

	// The legacy secret is untouched (not deleted, not labeled)
	legacyKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name))
	stored, err := client.GetSecret(ctx, legacyKey)
	require.NoError(t, err)
	assert.NotContains(t, stored.Labels, omv1.OpsManagerNamespaceLabel, "the operator must not label the legacy secret")
	assert.Equal(t, legacyAdminKeyData, secrets.DataToStringData(stored.Data))

	// No qualified secret was created from it
	qualifiedKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name))
	_, err = client.GetSecret(ctx, qualifiedKey)
	assert.True(t, secret.SecretNotExist(err), "qualified secret must not be created from an unlabeled legacy secret")

	// No user creation was attempted against Ops Manager
	assert.Equal(t, 0, initializer.numberOfCalls)
}

// TestMigrateLegacyAdminKeySecret_MismatchedLegacySecretLabel checks that a legacy
// Secret labeled for a different namespace is not migrated, not copied and not
// deleted: the migration fails closed.
func TestMigrateLegacyAdminKeySecret_MismatchedLegacySecretLabel(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: "someone-elses-namespace"})
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	status, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
	assert.NotEqual(t, workflow.OK(), status, "migration must fail closed for a legacy secret labeled with a foreign namespace")

	// The legacy secret is untouched (not deleted, label not rewritten)
	legacyKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name))
	stored, err := client.GetSecret(ctx, legacyKey)
	require.NoError(t, err)
	assert.Equal(t, "someone-elses-namespace", stored.Labels[omv1.OpsManagerNamespaceLabel])
	assert.Equal(t, legacyAdminKeyData, secrets.DataToStringData(stored.Data))

	// No qualified secret was created from it
	qualifiedKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name))
	_, err = client.GetSecret(ctx, qualifiedKey)
	assert.True(t, secret.SecretNotExist(err), "qualified secret must not be created from a foreign-labeled legacy secret")

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

// TestMigrateLegacyAdminKeySecret_QualifiedOnly_DuplicateNameNoLegacy checks that a
// valid qualified-key deployment is not blocked forever when another Ops Manager with
// the same CR name exists in a different namespace but the legacy Secret is absent:
// the duplicate-name conflict is only relevant while a legacy Secret exists.
func TestMigrateLegacyAdminKeySecret_QualifiedOnly_DuplicateNameNoLegacy(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	qualifiedData := map[string]string{util.OmPublicApiKey: "qualified-pk", util.OmPrivateKey: "qualified-sk"}
	qualifiedName := fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name)
	qualifiedSecret := adminKeySecret(OperatorNamespace, qualifiedName, qualifiedData,
		map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
	require.NoError(t, reconciler.client.CreateSecret(ctx, qualifiedSecret))

	// Another Ops Manager with the same CR name in a different namespace, but no legacy Secret
	otherOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	otherOm.Namespace = "other-namespace"
	require.NoError(t, client.Create(ctx, otherOm))

	reconciler.prepareOpsManagerAndAssertOK(ctx, t, testOm)

	// Qualified data is untouched
	data, err := secret.ReadStringData(ctx, client, kube.ObjectKey(OperatorNamespace, qualifiedName))
	require.NoError(t, err)
	assert.Equal(t, qualifiedData, data)

	// APIKeySecretName resolves to the qualified name
	assertQualifiedAPIKeySecretName(ctx, t, client, testOm)

	assert.Equal(t, 0, initializer.numberOfCalls)
}

func TestMigrateLegacyAdminKeySecret_Idempotent(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
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

	legacySecret := adminKeySecret(OperatorNamespace, "shared-om-admin-key", legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: victimOm.Namespace})
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

func TestAPIKeySecretName_LegacySecretWithValidLabel(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	_, client, _ := newMigrationTestReconciler(ctx, t, testOm)

	legacyName := fmt.Sprintf("%s-admin-key", testOm.Name)
	legacySecret := adminKeySecret(OperatorNamespace, legacyName, legacyAdminKeyData, map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
	require.NoError(t, client.CreateSecret(ctx, legacySecret))

	name, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	require.NoError(t, err)
	assert.Equal(t, legacyName, name)
}

func TestAPIKeySecretName_LegacySecretMissingLabel(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	_, client, _ := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData, nil)
	require.NoError(t, client.CreateSecret(ctx, legacySecret))

	// The unlabeled legacy secret must be rejected: resolve to the qualified name instead
	assertQualifiedAPIKeySecretName(ctx, t, client, testOm)
}

func TestAPIKeySecretName_LegacySecretMismatchedLabel(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	_, client, _ := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, fmt.Sprintf("%s-admin-key", testOm.Name), legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: "someone-elses-namespace"})
	require.NoError(t, client.CreateSecret(ctx, legacySecret))

	assertQualifiedAPIKeySecretName(ctx, t, client, testOm)
}

func TestAPIKeySecretName_QualifiedSecretMissingLabel(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	_, client, _ := newMigrationTestReconciler(ctx, t, testOm)

	qualifiedName := fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name)
	qualifiedSecret := adminKeySecret(OperatorNamespace, qualifiedName, legacyAdminKeyData, nil)
	require.NoError(t, client.CreateSecret(ctx, qualifiedSecret))

	name, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
	assert.Equal(t, qualifiedName, name)
}

func TestAPIKeySecretName_QualifiedSecretMismatchedLabel(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()
	_, client, _ := newMigrationTestReconciler(ctx, t, testOm)

	qualifiedName := fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name)
	qualifiedSecret := adminKeySecret(OperatorNamespace, qualifiedName, legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: "someone-elses-namespace"})
	require.NoError(t, client.CreateSecret(ctx, qualifiedSecret))

	_, err := testOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

// TestAPIKeySecretName_DelimiterCollision checks that two Ops Manager resources whose
// namespace-qualified secret names collide (namespace/name boundary ambiguity) cannot
// resolve each other's admin API key secret.
func TestAPIKeySecretName_DelimiterCollision(t *testing.T) {
	ctx := context.Background()

	// "ns-a" + "om-b" and "ns-a-om" + "b" both qualify to "ns-a-om-b-admin-key"
	omB := DefaultOpsManagerBuilder().SetNamespace("ns-a").SetName("om-b").Build()
	bCollision := DefaultOpsManagerBuilder().SetNamespace("ns-a-om").SetName("b").Build()
	require.Equal(t,
		fmt.Sprintf("%s-%s-admin-key", omB.Namespace, omB.Name),
		fmt.Sprintf("%s-%s-admin-key", bCollision.Namespace, bCollision.Name))

	_, client, _ := newMigrationTestReconciler(ctx, t, omB)
	qualifiedName := fmt.Sprintf("%s-%s-admin-key", omB.Namespace, omB.Name)

	// omB's qualified secret exists with omB's namespace label
	omBSecret := adminKeySecret(OperatorNamespace, qualifiedName, legacyAdminKeyData, map[string]string{omv1.OpsManagerNamespaceLabel: omB.Namespace})
	require.NoError(t, client.CreateSecret(ctx, omBSecret))

	name, err := omB.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	require.NoError(t, err)
	assert.Equal(t, qualifiedName, name)

	// The colliding OM must not be able to use omB's secret: the label does not match
	_, err = bCollision.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	assert.Error(t, err)
}

// TestAPIKeySecretName_LegacySecretNameCollision checks that a legacy (name-only)
// secret labeled for a different Ops Manager is not returned as the fallback for an
// Ops Manager whose legacy name collides with it (same name, different namespace).
func TestAPIKeySecretName_LegacySecretNameCollision(t *testing.T) {
	ctx := context.Background()

	victimOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	attackerOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	attackerOm.Namespace = "attacker-namespace"
	require.Equal(t, victimOm.Name, attackerOm.Name)
	require.NotEqual(t, victimOm.Namespace, attackerOm.Namespace)

	_, client, _ := newMigrationTestReconciler(ctx, t, victimOm)
	legacyName := fmt.Sprintf("%s-admin-key", victimOm.Name)

	// Victim's legacy secret labeled for the victim's namespace
	legacySecret := adminKeySecret(OperatorNamespace, legacyName, legacyAdminKeyData, map[string]string{omv1.OpsManagerNamespaceLabel: victimOm.Namespace})
	require.NoError(t, client.CreateSecret(ctx, legacySecret))

	// The victim resolves to its legacy secret...
	name, err := victimOm.APIKeySecretName(ctx, secrets.SecretClient{KubeClient: client}, "")
	require.NoError(t, err)
	assert.Equal(t, legacyName, name)

	// ...but the attacker, whose legacy secret name collides, must not
	assertQualifiedAPIKeySecretName(ctx, t, client, attackerOm)
}

// TestMigrateLegacyAdminKeySecret_DuplicateNameConflict checks that the migration
// fails closed when another Ops Manager with the same CR name exists in a different
// namespace: the legacy Secret is left untouched (not copied, not deleted).
func TestMigrateLegacyAdminKeySecret_DuplicateNameConflict(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, testOm)

	legacySecret := adminKeySecret(OperatorNamespace, "shared-om-admin-key", legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: testOm.Namespace})
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	// Another Ops Manager with the same CR name in a different namespace
	otherOm := DefaultOpsManagerBuilder().SetName("shared-om").Build()
	otherOm.Namespace = "other-namespace"
	require.NoError(t, client.Create(ctx, otherOm))

	status, _ := reconciler.prepareOpsManager(ctx, testOm, testOm.CentralURL(), zap.S())
	assert.NotEqual(t, workflow.OK(), status, "migration must fail closed on a duplicate OpsManager name")

	// The legacy secret is untouched
	legacyKey := kube.ObjectKey(OperatorNamespace, "shared-om-admin-key")
	stored, err := client.GetSecret(ctx, legacyKey)
	require.NoError(t, err)
	assert.Equal(t, legacyAdminKeyData, secrets.DataToStringData(stored.Data))
	assert.Equal(t, testOm.Namespace, stored.Labels[omv1.OpsManagerNamespaceLabel])

	// No qualified secret was created
	qualifiedKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-%s-admin-key", testOm.Namespace, testOm.Name))
	_, err = client.GetSecret(ctx, qualifiedKey)
	assert.True(t, secret.SecretNotExist(err), "qualified secret must not be created on a duplicate-name conflict")

	assert.Equal(t, 0, initializer.numberOfCalls)
}

// TestMigrateLegacyAdminKeySecret_QualifiedNameDelimiterConflict checks that the
// migration fails closed when another Ops Manager's qualified admin-key name
// collides with this one's (namespace/name boundary ambiguity): secrets are
// left untouched.
func TestMigrateLegacyAdminKeySecret_QualifiedNameDelimiterConflict(t *testing.T) {
	ctx := context.Background()

	// "ns-a" + "om-b" and "ns-a-om" + "b" both qualify to "ns-a-om-b-admin-key"
	omB := DefaultOpsManagerBuilder().SetNamespace("ns-a").SetName("om-b").Build()
	bCollision := DefaultOpsManagerBuilder().SetNamespace("ns-a-om").SetName("b").Build()
	require.Equal(t,
		fmt.Sprintf("%s-%s-admin-key", omB.Namespace, omB.Name),
		fmt.Sprintf("%s-%s-admin-key", bCollision.Namespace, bCollision.Name))

	reconciler, client, initializer := newMigrationTestReconciler(ctx, t, omB)

	legacySecret := adminKeySecret(OperatorNamespace, "om-b-admin-key", legacyAdminKeyData,
		map[string]string{omv1.OpsManagerNamespaceLabel: omB.Namespace})
	require.NoError(t, reconciler.client.CreateSecret(ctx, legacySecret))

	require.NoError(t, client.Create(ctx, bCollision))

	status, _ := reconciler.prepareOpsManager(ctx, omB, omB.CentralURL(), zap.S())
	assert.NotEqual(t, workflow.OK(), status, "migration must fail closed on a qualified-name delimiter collision")

	// The legacy secret is untouched
	legacyKey := kube.ObjectKey(OperatorNamespace, "om-b-admin-key")
	stored, err := client.GetSecret(ctx, legacyKey)
	require.NoError(t, err)
	assert.Equal(t, legacyAdminKeyData, secrets.DataToStringData(stored.Data))
	assert.Equal(t, omB.Namespace, stored.Labels[omv1.OpsManagerNamespaceLabel])

	// No qualified secret was created
	qualifiedKey := kube.ObjectKey(OperatorNamespace, fmt.Sprintf("%s-%s-admin-key", omB.Namespace, omB.Name))
	_, err = client.GetSecret(ctx, qualifiedKey)
	assert.True(t, secret.SecretNotExist(err), "qualified secret must not be created on a name collision")

	assert.Equal(t, 0, initializer.numberOfCalls)
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
