package certs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const testBundlePropagationDelay = 2 * time.Minute

// newReissueTestClient returns a client that keeps Certificate status separate, as cert-manager does.
func newReissueTestClient(t *testing.T, objects ...client.Object) kubernetesClient.Client {
	t.Helper()
	scheme, err := v1.SchemeBuilder.Build()
	require.NoError(t, err)
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, certmanagerv1.AddToScheme(scheme))
	return kubernetesClient.NewClient(fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&certmanagerv1.Certificate{}).
		WithObjects(objects...).
		Build())
}

// publishBundle writes a CA bundle ConfigMap whose latest contents were published at publishedAt.
func publishBundle(t *testing.T, c kubernetesClient.Client, name string, publishedAt time.Time) {
	t.Helper()
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: managedTestNamespace,
			Annotations: map[string]string{
				util.CABundlePublishedAtAnnotation: publishedAt.UTC().Format(time.RFC3339),
			},
		},
		Data: map[string]string{util.LatestHashSecretKey: "some-hash"},
	}
	require.NoError(t, c.Create(context.Background(), &cm))
}

// prepareStaleLeaf writes the CA secret with the current root, a leaf secret signed by the old root
// and the Certificate object cert-manager would have created for the leaf.
func prepareStaleLeaf(t *testing.T, c kubernetesClient.Client, owner CertificateOwner, cat certCategory, oldRoot *testCA, leafSecretName string) {
	t.Helper()
	ctx := context.Background()
	currentRoot := newTestCA(t, "current-root")

	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: managedCACertName(owner, cat), Namespace: managedTestNamespace},
		Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(currentRoot.pem)},
	}))
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: leafSecretName, Namespace: managedTestNamespace},
		Data:       map[string][]byte{corev1.TLSCertKey: oldRoot.issueLeaf(t, leafSecretName)},
	}))
	require.NoError(t, c.Create(ctx, &certmanagerv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{Name: leafSecretName, Namespace: managedTestNamespace, Generation: 1},
	}))
}

// prepareCurrentLeaf writes the CA secret and a leaf signed by that same current root.
func prepareCurrentLeaf(t *testing.T, c kubernetesClient.Client, owner CertificateOwner, cat certCategory, leafSecretName string) {
	t.Helper()
	ctx := context.Background()
	currentRoot := newTestCA(t, "current-root")

	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: managedCACertName(owner, cat), Namespace: managedTestNamespace},
		Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(currentRoot.pem)},
	}))
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: leafSecretName, Namespace: managedTestNamespace},
		Data:       map[string][]byte{corev1.TLSCertKey: currentRoot.issueLeaf(t, leafSecretName)},
	}))
	require.NoError(t, c.Create(ctx, &certmanagerv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{Name: leafSecretName, Namespace: managedTestNamespace, Generation: 1},
	}))
}

func assertManagedTestCertificateIssuing(t *testing.T, c kubernetesClient.Client, name string, expected bool) {
	t.Helper()
	condition := certificateIssuingCondition(getManagedCertificate(t, context.Background(), c, name))
	if !expected {
		assert.Nil(t, condition, "certificate %s must not be reissued", name)
		return
	}
	require.NotNil(t, condition, "certificate %s must be reissued", name)
	assert.Equal(t, cmmeta.ConditionTrue, condition.Status)
	assert.Equal(t, reasonManagedCARotated, condition.Reason)
}

func TestReissueAppDBLeavesOnRotatedSelfSignedCA(t *testing.T) {
	oldRoot := newTestCA(t, "old-root")

	newFixture := func(t *testing.T) (kubernetesClient.Client, *omv1.MongoDBOpsManager) {
		t.Helper()
		om := managedTestOM()
		return newReissueTestClient(t, om), om
	}

	t.Run("reissues a leaf still signed by the previous root once the bundle has propagated", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := appDBManagedCertificateOwner{om}
		opts := AppDBManagedCertConfig(om)
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.CertSecretName)
		publishBundle(t, c, ManagedServerCABundleConfigMapName(om.AppDBName()), time.Now().Add(-10*time.Minute))

		wait, err := ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, c, om, []Options{opts}, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.CertSecretName, true)
	})

	t.Run("waits while the widened bundle is younger than the propagation delay", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := appDBManagedCertificateOwner{om}
		opts := AppDBManagedCertConfig(om)
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.CertSecretName)
		publishBundle(t, c, ManagedServerCABundleConfigMapName(om.AppDBName()), time.Now())

		wait, err := ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, c, om, []Options{opts}, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Positive(t, wait)
		assert.LessOrEqual(t, wait, testBundlePropagationDelay)
		assertManagedTestCertificateIssuing(t, c, opts.CertSecretName, false)
	})

	t.Run("a bundle without a publication timestamp is treated as just published", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := appDBManagedCertificateOwner{om}
		opts := AppDBManagedCertConfig(om)
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.CertSecretName)
		require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ManagedServerCABundleConfigMapName(om.AppDBName()), Namespace: managedTestNamespace},
			Data:       map[string]string{util.LatestHashSecretKey: "some-hash"},
		}))

		wait, err := ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, c, om, []Options{opts}, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Equal(t, testBundlePropagationDelay, wait)
		assertManagedTestCertificateIssuing(t, c, opts.CertSecretName, false)
	})

	t.Run("a leaf already signed by the current root is left alone and never waits", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := appDBManagedCertificateOwner{om}
		opts := AppDBManagedCertConfig(om)
		prepareCurrentLeaf(t, c, owner, categoryServer, opts.CertSecretName)
		// No bundle: a steady-state AppDB must not need it, and must not requeue.
		wait, err := ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, c, om, []Options{opts}, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.CertSecretName, false)
	})

	t.Run("a user issuer is never touched", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		om.Spec.AppDB.Security.ManagedCertificate.Server = &mdbv1.CertificateConfig{
			IssuerRef: &mdbv1.IssuerRef{Name: "user-issuer", Kind: "Issuer"},
		}
		owner := appDBManagedCertificateOwner{om}
		opts := AppDBManagedCertConfig(om)
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.CertSecretName)

		wait, err := ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, c, om, []Options{opts}, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.CertSecretName, false)
	})

	t.Run("managed certificates disabled is a no-op", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		om.Spec.AppDB.Security.ManagedCertificate = nil
		opts := AppDBManagedCertConfig(om)

		wait, err := ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, c, om, []Options{opts}, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
	})
}

func TestReissueServerCertificateOnRotatedSelfSignedCA(t *testing.T) {
	oldRoot := newTestCA(t, "old-root")
	opts := serverCertificateOptions()

	newFixture := func(t *testing.T) (kubernetesClient.Client, *omv1.MongoDBOpsManager) {
		t.Helper()
		om := managedTestOM()
		om.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
		return newReissueTestClient(t, om), om
	}

	t.Run("reissues a leaf still signed by the previous root once the bundle has propagated", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := serverCertificateOwner{ObjectOwner: om, name: opts.NamingBase, security: &mdbv1.Security{
			ManagedCertificate: &mdbv1.ManagedCertificate{Server: &om.Spec.Security.ManagedCertificate.CertificateConfig},
		}}
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.SecretName)
		publishBundle(t, c, opts.CABundleName, time.Now().Add(-10*time.Minute))

		wait, err := ReissueServerCertificateOnRotatedSelfSignedCA(ctx, c, om, om.Spec.Security.ManagedCertificate, opts, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.SecretName, true)
	})

	t.Run("waits while the widened bundle is younger than the propagation delay", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := serverCertificateOwner{ObjectOwner: om, name: opts.NamingBase, security: &mdbv1.Security{
			ManagedCertificate: &mdbv1.ManagedCertificate{Server: &om.Spec.Security.ManagedCertificate.CertificateConfig},
		}}
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.SecretName)
		publishBundle(t, c, opts.CABundleName, time.Now())

		wait, err := ReissueServerCertificateOnRotatedSelfSignedCA(ctx, c, om, om.Spec.Security.ManagedCertificate, opts, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Positive(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.SecretName, false)
	})

	t.Run("a leaf already signed by the current root is left alone and never waits", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		owner := serverCertificateOwner{ObjectOwner: om, name: opts.NamingBase, security: &mdbv1.Security{
			ManagedCertificate: &mdbv1.ManagedCertificate{Server: &om.Spec.Security.ManagedCertificate.CertificateConfig},
		}}
		prepareCurrentLeaf(t, c, owner, categoryServer, opts.SecretName)

		wait, err := ReissueServerCertificateOnRotatedSelfSignedCA(ctx, c, om, om.Spec.Security.ManagedCertificate, opts, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.SecretName, false)
	})

	t.Run("a user issuer is never touched", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)
		cfg := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
			IssuerRef: &mdbv1.IssuerRef{Name: "user-issuer", Kind: "Issuer"},
		}}
		owner := serverCertificateOwner{ObjectOwner: om, name: opts.NamingBase, security: &mdbv1.Security{
			ManagedCertificate: &mdbv1.ManagedCertificate{Server: &cfg.CertificateConfig},
		}}
		prepareStaleLeaf(t, c, owner, categoryServer, oldRoot, opts.SecretName)

		wait, err := ReissueServerCertificateOnRotatedSelfSignedCA(ctx, c, om, cfg, opts, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
		assertManagedTestCertificateIssuing(t, c, opts.SecretName, false)
	})

	t.Run("a nil configuration is a no-op", func(t *testing.T) {
		ctx := context.Background()
		c, om := newFixture(t)

		wait, err := ReissueServerCertificateOnRotatedSelfSignedCA(ctx, c, om, nil, opts, testBundlePropagationDelay, managedTestLogger())
		require.NoError(t, err)
		assert.Zero(t, wait)
	})
}

func TestCABundlePublishedAtIsKeptWhileTheBundleIsUnchanged(t *testing.T) {
	ctx := context.Background()
	serverCA := newTestCA(t, "server-ca")
	originalNow := now
	t.Cleanup(func() { now = originalNow })

	c, _ := mock.NewDefaultFakeClient()
	owner, opts, setCAs := managedCATestFixture(t, c)
	setCAs(serverCA.pem, serverCA.pem)

	publishedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now = func() time.Time { return publishedAt }
	ensureBundles(t, c, owner, opts, false)

	cm, err := c.GetConfigMap(ctx, serverCMKey)
	require.NoError(t, err)
	assert.Equal(t, publishedAt.Format(time.RFC3339), cm.Annotations[util.CABundlePublishedAtAnnotation])

	// The bundle does not change, so the timestamp is preserved and the ConfigMap is not rewritten.
	later := publishedAt.Add(time.Hour)
	now = func() time.Time { return later }
	ensureBundles(t, c, owner, opts, false)
	unchanged, err := c.GetConfigMap(ctx, serverCMKey)
	require.NoError(t, err)
	assert.Equal(t, publishedAt.Format(time.RFC3339), unchanged.Annotations[util.CABundlePublishedAtAnnotation])
	assert.Equal(t, cm.ResourceVersion, unchanged.ResourceVersion)

	// A new bundle content refreshes the timestamp.
	rotated := newTestCA(t, "server-ca-2")
	setCAs(rotated.pem, rotated.pem)
	ensureBundles(t, c, owner, opts, false)
	republished, err := c.GetConfigMap(ctx, serverCMKey)
	require.NoError(t, err)
	assert.Equal(t, later.Format(time.RFC3339), republished.Annotations[util.CABundlePublishedAtAnnotation])
}

func TestCategoryLeafNeedsReissue(t *testing.T) {
	ctx := context.Background()

	t.Run("a leaf secret that does not exist yet is skipped", func(t *testing.T) {
		om := managedTestOM()
		c := newReissueTestClient(t, om)
		owner := appDBManagedCertificateOwner{om}
		currentRoot := newTestCA(t, "current-root")
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: managedCACertName(owner, categoryServer), Namespace: managedTestNamespace},
			Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(currentRoot.pem)},
		}))

		needsReissue, err := categoryLeafNeedsReissue(ctx, c, owner, categoryServer, []Options{AppDBManagedCertConfig(om)}, managedTestLogger())
		require.NoError(t, err)
		assert.False(t, needsReissue)
	})

	t.Run("a missing leaf secret does not hide a stale one", func(t *testing.T) {
		om := managedTestOM()
		c := newReissueTestClient(t, om)
		owner := appDBManagedCertificateOwner{om}
		prepareStaleLeaf(t, c, owner, categoryServer, newTestCA(t, "old-root"), "stale-leaf")

		needsReissue, err := categoryLeafNeedsReissue(ctx, c, owner, categoryServer,
			[]Options{{CertSecretName: "missing-leaf"}, {CertSecretName: "stale-leaf"}}, managedTestLogger())
		require.NoError(t, err)
		assert.True(t, needsReissue)
	})

	t.Run("a missing CA secret reports no re-issue", func(t *testing.T) {
		om := managedTestOM()
		c := newReissueTestClient(t, om)
		owner := appDBManagedCertificateOwner{om}
		opts := AppDBManagedCertConfig(om)
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: opts.CertSecretName, Namespace: managedTestNamespace},
			Data:       map[string][]byte{corev1.TLSCertKey: newTestCA(t, "old-root").issueLeaf(t, opts.CertSecretName)},
		}))

		needsReissue, err := categoryLeafNeedsReissue(ctx, c, owner, categoryServer, []Options{opts}, managedTestLogger())
		require.NoError(t, err)
		assert.False(t, needsReissue)
	})
}

func TestCABundlePropagationWait(t *testing.T) {
	ctx := context.Background()
	const bundleName = "example-bundle"

	createBundle := func(t *testing.T, c kubernetesClient.Client, annotations map[string]string) {
		t.Helper()
		require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: bundleName, Namespace: managedTestNamespace, Annotations: annotations},
			Data:       map[string]string{util.LatestHashSecretKey: "some-hash"},
		}))
	}

	t.Run("a malformed publication timestamp is treated as just published", func(t *testing.T) {
		c := newReissueTestClient(t)
		createBundle(t, c, map[string]string{util.CABundlePublishedAtAnnotation: "not-a-timestamp"})

		wait, ready, err := caBundlePropagationWait(ctx, c, managedTestNamespace, bundleName, testBundlePropagationDelay)
		require.NoError(t, err)
		assert.False(t, ready)
		assert.Equal(t, testBundlePropagationDelay, wait)
	})

	t.Run("a missing publication timestamp is treated as just published", func(t *testing.T) {
		c := newReissueTestClient(t)
		createBundle(t, c, nil)

		wait, ready, err := caBundlePropagationWait(ctx, c, managedTestNamespace, bundleName, testBundlePropagationDelay)
		require.NoError(t, err)
		assert.False(t, ready)
		assert.Equal(t, testBundlePropagationDelay, wait)
	})

	t.Run("a bundle published long enough ago is ready", func(t *testing.T) {
		c := newReissueTestClient(t)
		createBundle(t, c, map[string]string{
			util.CABundlePublishedAtAnnotation: time.Now().Add(-testBundlePropagationDelay - time.Minute).UTC().Format(time.RFC3339),
		})

		wait, ready, err := caBundlePropagationWait(ctx, c, managedTestNamespace, bundleName, testBundlePropagationDelay)
		require.NoError(t, err)
		assert.True(t, ready)
		assert.Zero(t, wait)
	})

	t.Run("a missing ConfigMap is an error", func(t *testing.T) {
		c := newReissueTestClient(t)

		wait, ready, err := caBundlePropagationWait(ctx, c, managedTestNamespace, bundleName, testBundlePropagationDelay)
		require.Error(t, err)
		assert.Contains(t, err.Error(), bundleName)
		assert.False(t, ready)
		assert.Zero(t, wait)
	})
}
