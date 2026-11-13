package operator

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	certspkg "github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// testCA is a minimal CA with a key signer used for test fixtures.
type rotationTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newRotationTestCA(t *testing.T, commonName string) *rotationTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &rotationTestCA{
		cert: cert,
		key:  key,
		pem:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// newRotationTestClient returns a fake client with cert-manager Certificate status subresource
// and the OM/AppDB/cert-manager schemes registered. Using the shared newManagedCertificateKubeClient
// would not support Certificate status updates.
func newRotationTestClient(t *testing.T, objects ...client.Object) kubernetesClient.Client {
	t.Helper()
	scheme, err := v1.SchemeBuilder.Build()
	require.NoError(t, err)
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, certmanagerv1.AddToScheme(scheme))
	return kubernetesClient.NewClient(fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&certmanagerv1.Certificate{}).
		WithObjects(objects...).
		Build())
}

func (ca *rotationTestCA) issueLeaf(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSmallestPositiveWait(t *testing.T) {
	for _, tc := range []struct {
		name   string
		waits  []time.Duration
		expect time.Duration
	}{
		{name: "empty slice", waits: nil, expect: 0},
		{name: "all zeros", waits: []time.Duration{0, 0}, expect: 0},
		{name: "single positive", waits: []time.Duration{5 * time.Second}, expect: 5 * time.Second},
		{name: "positive and zero", waits: []time.Duration{0, 3 * time.Second, 0}, expect: 3 * time.Second},
		{name: "multiple positives", waits: []time.Duration{10 * time.Second, 3 * time.Second, 7 * time.Second}, expect: 3 * time.Second},
		{name: "all positives", waits: []time.Duration{10 * time.Second, 3 * time.Second}, expect: 3 * time.Second},
		{name: "negative values ignored", waits: []time.Duration{-time.Second, 5 * time.Second}, expect: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, smallestPositiveWait(tc.waits))
		})
	}
}

func TestReissueManagedCertificatesOnRotatedCA_CertManagerNotReady(t *testing.T) {
	ctx := t.Context()
	om := newManagedCertificateTestOM()
	om.Spec.AppDB.Security.ManagedCertificate = nil
	c := newManagedCertificateKubeClient(t, om)
	r := newManagedCertificateTestReconciler(ctx, c)
	r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: false}

	wait := r.reissueManagedCertificatesOnRotatedCA(ctx, om, zap.S())
	assert.Zero(t, wait, "must return 0 when cert-manager is not Ready")
}

func TestReissueManagedCertificatesOnRotatedCA_OMSelfSigned(t *testing.T) {
	const ns = "my-namespace"
	omName := "managed-om"
	secretName := omName + "-managed-om-cert"
	caSecretName := omName + "-managed-om-server-ca"
	bundleName := omName + "-managed-om-ca-bundle"

	oldRoot := newRotationTestCA(t, "old-root")
	newRoot := newRotationTestCA(t, "new-root")
	oldLeafPEM := oldRoot.issueLeaf(t, secretName)

	makeOM := func() *omv1.MongoDBOpsManager {
		return &omv1.MongoDBOpsManager{
			ObjectMeta: metav1.ObjectMeta{
				Name:      omName,
				Namespace: ns,
				UID:       types.UID("om-uid"),
			},
			Spec: omv1.MongoDBOpsManagerSpec{
				Version:     "7.0.0",
				AppDB:       nil,
				AdminSecret: "om-admin",
				Security: &omv1.MongoDBOpsManagerSecurity{
					ManagedCertificate: &omv1.ManagedCertificate{},
				},
			},
		}
	}

	t.Run("reissues a stale leaf once the bundle has propagated", func(t *testing.T) {
		ctx := t.Context()
		om := makeOM()
		// CA secret holds the NEW root.
		c := newRotationTestClient(t, om,
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: caSecretName, Namespace: ns},
				Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(newRoot.pem)},
			},
			// Leaf secret signed by the OLD root.
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
				Data:       map[string][]byte{corev1.TLSCertKey: oldLeafPEM},
			},
			// Certificate object from cert-manager.
			&certmanagerv1.Certificate{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Generation: 1},
			},
			// Bundle ConfigMap with an old published-at annotation.
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: ns,
					Annotations: map[string]string{
						util.CABundlePublishedAtAnnotation: time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
					},
				},
				Data: map[string]string{util.LatestHashSecretKey: "some-hash"},
			},
		)
		r := newManagedCertificateTestReconciler(ctx, c)
		r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}

		wait := r.reissueManagedCertificatesOnRotatedCA(ctx, om, zap.S())
		assert.Zero(t, wait, "must return 0 when the leaf is re-issued")

		// The leaf Certificate must have Issuing=True with reason ManagedCARotated.
		var updatedCert certmanagerv1.Certificate
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, &updatedCert))
		cond := findIssuingCondition(updatedCert.Status.Conditions)
		require.NotNil(t, cond, "Certificate must have an Issuing condition")
		assert.Equal(t, cmmeta.ConditionTrue, cond.Status)
		assert.Equal(t, "ManagedCARotated", cond.Reason)
	})

	t.Run("waits when the bundle annotation is fresh", func(t *testing.T) {
		ctx := t.Context()
		om := makeOM()
		c := newRotationTestClient(t, om,
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: caSecretName, Namespace: ns},
				Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(newRoot.pem)},
			},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
				Data:       map[string][]byte{corev1.TLSCertKey: oldLeafPEM},
			},
			&certmanagerv1.Certificate{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Generation: 1},
			},
			// Bundle ConfigMap with a just-published annotation.
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: ns,
					Annotations: map[string]string{
						util.CABundlePublishedAtAnnotation: time.Now().UTC().Format(time.RFC3339),
					},
				},
				Data: map[string]string{util.LatestHashSecretKey: "some-hash"},
			},
		)
		r := newManagedCertificateTestReconciler(ctx, c)
		r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}

		wait := r.reissueManagedCertificatesOnRotatedCA(ctx, om, zap.S())
		assert.Positive(t, wait, "must return a positive wait for a fresh bundle")
		assert.LessOrEqual(t, wait, 2*time.Minute, "wait must not exceed the propagation delay")

		// The leaf Certificate must NOT have Issuing=True.
		var unchangedCert certmanagerv1.Certificate
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, &unchangedCert))
		cond := findIssuingCondition(unchangedCert.Status.Conditions)
		assert.Nil(t, cond, "Certificate must not have an Issuing condition")
	})
}

// findIssuingCondition returns the Issuing condition from a list of metav1 conditions.
func findIssuingCondition(conditions []certmanagerv1.CertificateCondition) *certmanagerv1.CertificateCondition {
	for i := range conditions {
		if conditions[i].Type == certmanagerv1.CertificateConditionIssuing {
			return &conditions[i]
		}
	}
	return nil
}

func TestReissueManagedCertificatesOnRotatedCA_OMAppDBBackup(t *testing.T) {
	ctx := t.Context()
	c := newManagedCertificateKubeClient(t)
	r := newManagedCertificateTestReconciler(ctx, c)
	r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: false}

	// When cert-manager is not Ready, the function must short-circuit to 0
	// regardless of the other arguments.
	om := newManagedCertificateTestOM()
	wait := r.reissueManagedCertificatesOnRotatedCA(ctx, om, zap.S())
	assert.Zero(t, wait)
}

func TestRequeueForCertificateReissue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result reconcile.Result
		wait   time.Duration
		expect reconcile.Result
	}{
		{name: "no wait leaves an empty result unchanged", result: reconcile.Result{}, wait: 0, expect: reconcile.Result{}},
		{name: "no wait leaves an existing requeue unchanged", result: reconcile.Result{RequeueAfter: time.Minute}, wait: 0, expect: reconcile.Result{RequeueAfter: time.Minute}},
		{name: "negative wait is ignored", result: reconcile.Result{}, wait: -time.Second, expect: reconcile.Result{}},
		{name: "wait without an existing requeue is set", result: reconcile.Result{}, wait: 30 * time.Second, expect: reconcile.Result{RequeueAfter: 30 * time.Second}},
		{name: "wait shorter than the existing requeue wins", result: reconcile.Result{RequeueAfter: time.Minute}, wait: 30 * time.Second, expect: reconcile.Result{RequeueAfter: 30 * time.Second}},
		{name: "wait longer than the existing requeue is ignored", result: reconcile.Result{RequeueAfter: 10 * time.Second}, wait: 30 * time.Second, expect: reconcile.Result{RequeueAfter: 10 * time.Second}},
		{name: "wait equal to the existing requeue keeps it", result: reconcile.Result{RequeueAfter: 30 * time.Second}, wait: 30 * time.Second, expect: reconcile.Result{RequeueAfter: 30 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, requeueForCertificateReissue(tc.result, tc.wait))
		})
	}
}

// utRotationTestOM returns an Ops Manager with managed certificates enabled only for the selected
// components. Backup is enabled in the spec either way.
func utRotationTestOM(appDB, opsManager, backup bool) *omv1.MongoDBOpsManager {
	om := newManagedCertificateTestOM()
	om.Spec.Security = nil
	if opsManager {
		om.Spec.Security = &omv1.MongoDBOpsManagerSecurity{ManagedCertificate: &omv1.ManagedCertificate{}}
	}
	if appDB {
		om.Spec.AppDB.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
	} else if om.Spec.AppDB.Security != nil {
		om.Spec.AppDB.Security.ManagedCertificate = nil
	}
	if backup {
		om.Spec.Backup.ManagedCertificate = &omv1.ManagedCertificate{}
	}
	return om
}

// utStaleLeafObjects returns a component's CA secret holding newRoot, its leaf secret signed by
// oldRoot, the leaf's Certificate and the component's CA bundle ConfigMap published at publishedAt.
func utStaleLeafObjects(t *testing.T, namespace, caSecretName, leafSecretName, bundleName string, oldRoot, newRoot *rotationTestCA, publishedAt time.Time) []client.Object {
	t.Helper()
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: caSecretName, Namespace: namespace},
			Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte(newRoot.pem)},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: leafSecretName, Namespace: namespace},
			Data:       map[string][]byte{corev1.TLSCertKey: oldRoot.issueLeaf(t, leafSecretName)},
		},
		&certmanagerv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{Name: leafSecretName, Namespace: namespace, Generation: 1},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bundleName,
				Namespace: namespace,
				Annotations: map[string]string{
					util.CABundlePublishedAtAnnotation: publishedAt.UTC().Format(time.RFC3339),
				},
			},
			Data: map[string]string{util.LatestHashSecretKey: "some-hash"},
		},
	}
}

// utAppDBStaleLeafObjects returns the stale-leaf fixture of the internal AppDB's server certificate.
func utAppDBStaleLeafObjects(t *testing.T, om *omv1.MongoDBOpsManager, oldRoot, newRoot *rotationTestCA, publishedAt time.Time) []client.Object {
	t.Helper()
	return utStaleLeafObjects(t, om.Namespace,
		om.AppDBName()+"-server-ca",
		certspkg.AppDBManagedCertConfig(om).CertSecretName,
		certspkg.ManagedServerCABundleConfigMapName(om.AppDBName()),
		oldRoot, newRoot, publishedAt)
}

// utBackupStaleLeafObjects returns the stale-leaf fixture of the queryable backup server certificate.
func utBackupStaleLeafObjects(t *testing.T, om *omv1.MongoDBOpsManager, oldRoot, newRoot *rotationTestCA, publishedAt time.Time) []client.Object {
	t.Helper()
	return utStaleLeafObjects(t, om.Namespace,
		om.Name+"-managed-backup-server-ca",
		om.Name+"-managed-backup-cert",
		om.ManagedBackupCABundleConfigMapName(),
		oldRoot, newRoot, publishedAt)
}

// utOMStaleLeafObjects returns the stale-leaf fixture of the Ops Manager app server certificate.
func utOMStaleLeafObjects(t *testing.T, om *omv1.MongoDBOpsManager, oldRoot, newRoot *rotationTestCA, publishedAt time.Time) []client.Object {
	t.Helper()
	return utStaleLeafObjects(t, om.Namespace,
		om.Name+"-managed-om-server-ca",
		om.Name+"-managed-om-cert",
		om.Name+"-managed-om-ca-bundle",
		oldRoot, newRoot, publishedAt)
}

func utIssuingCondition(t *testing.T, c kubernetesClient.Client, namespace, name string) *certmanagerv1.CertificateCondition {
	t.Helper()
	cert := certmanagerv1.Certificate{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, &cert))
	return findIssuingCondition(cert.Status.Conditions)
}

func utAssertReissued(t *testing.T, c kubernetesClient.Client, namespace, name string) {
	t.Helper()
	cond := utIssuingCondition(t, c, namespace, name)
	require.NotNil(t, cond, "Certificate %s must have an Issuing condition", name)
	assert.Equal(t, cmmeta.ConditionTrue, cond.Status)
	assert.Equal(t, "ManagedCARotated", cond.Reason)
}

func utAssertNotReissued(t *testing.T, c kubernetesClient.Client, namespace, name string) {
	t.Helper()
	assert.Nil(t, utIssuingCondition(t, c, namespace, name), "Certificate %s must not have an Issuing condition", name)
}

func utRotationTestReconciler(t *testing.T, c kubernetesClient.Client) *OpsManagerReconciler {
	t.Helper()
	r := newManagedCertificateTestReconciler(t.Context(), c)
	r.certManagerReadiness = certspkg.CertManagerReadiness{Ready: true}
	return r
}

func TestReissueManagedCertificatesOnRotatedCA_AppDB(t *testing.T) {
	oldRoot := newRotationTestCA(t, "old-appdb-root")
	newRoot := newRotationTestCA(t, "new-appdb-root")
	propagated := time.Now().Add(-10 * time.Minute)

	t.Run("reissues the AppDB leaf once the bundle has propagated", func(t *testing.T) {
		om := utRotationTestOM(true, false, false)
		c := newRotationTestClient(t, append([]client.Object{om}, utAppDBStaleLeafObjects(t, om, oldRoot, newRoot, propagated)...)...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Zero(t, wait)
		utAssertReissued(t, c, om.Namespace, certspkg.AppDBManagedCertConfig(om).CertSecretName)
	})

	t.Run("reissues the AppDB leaf when the StatefulSet is owned by this Ops Manager", func(t *testing.T) {
		om := utRotationTestOM(true, false, false)
		sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name:      om.Spec.AppDB.Name(),
			Namespace: om.Namespace,
			Labels:    map[string]string{util.MongoDBOpsManagerResourceOwnerLabel: om.Name},
		}}
		c := newRotationTestClient(t, append([]client.Object{om, sts}, utAppDBStaleLeafObjects(t, om, oldRoot, newRoot, propagated)...)...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Zero(t, wait)
		utAssertReissued(t, c, om.Namespace, certspkg.AppDBManagedCertConfig(om).CertSecretName)
	})

	t.Run("skips the AppDB when its StatefulSet is owned by another resource", func(t *testing.T) {
		om := utRotationTestOM(true, false, false)
		sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name:      om.Spec.AppDB.Name(),
			Namespace: om.Namespace,
			Labels:    map[string]string{util.MongoDBResourceOwnerLabel: "other-mdb"},
		}}
		c := newRotationTestClient(t, append([]client.Object{om, sts}, utAppDBStaleLeafObjects(t, om, oldRoot, newRoot, propagated)...)...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Zero(t, wait)
		utAssertNotReissued(t, c, om.Namespace, certspkg.AppDBManagedCertConfig(om).CertSecretName)
	})

	t.Run("waits while the AppDB bundle is younger than the propagation delay", func(t *testing.T) {
		om := utRotationTestOM(true, false, false)
		c := newRotationTestClient(t, append([]client.Object{om}, utAppDBStaleLeafObjects(t, om, oldRoot, newRoot, time.Now())...)...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Positive(t, wait)
		assert.LessOrEqual(t, wait, managedCABundlePropagationDelay)
		utAssertNotReissued(t, c, om.Namespace, certspkg.AppDBManagedCertConfig(om).CertSecretName)
	})
}

func TestReissueManagedCertificatesOnRotatedCA_Backup(t *testing.T) {
	oldRoot := newRotationTestCA(t, "old-backup-root")
	newRoot := newRotationTestCA(t, "new-backup-root")
	propagated := time.Now().Add(-10 * time.Minute)

	t.Run("reissues the backup leaf once the bundle has propagated", func(t *testing.T) {
		om := utRotationTestOM(false, false, true)
		c := newRotationTestClient(t, append([]client.Object{om}, utBackupStaleLeafObjects(t, om, oldRoot, newRoot, propagated)...)...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Zero(t, wait)
		utAssertReissued(t, c, om.Namespace, om.QueryableBackupCertificateSecretName())
	})

	t.Run("leaves the backup leaf alone while backup is disabled", func(t *testing.T) {
		om := utRotationTestOM(false, false, true)
		om.Spec.Backup.Enabled = false
		c := newRotationTestClient(t, append([]client.Object{om}, utBackupStaleLeafObjects(t, om, oldRoot, newRoot, propagated)...)...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Zero(t, wait)
		utAssertNotReissued(t, c, om.Namespace, om.QueryableBackupCertificateSecretName())
	})

	t.Run("returns the smallest positive wait across components while bundles are fresh", func(t *testing.T) {
		om := utRotationTestOM(true, true, true)
		start := time.Now()
		objects := []client.Object{om}
		// Remaining waits: AppDB ~60s, Ops Manager ~90s, backup ~30s.
		objects = append(objects, utAppDBStaleLeafObjects(t, om, oldRoot, newRoot, start.Add(-60*time.Second))...)
		objects = append(objects, utOMStaleLeafObjects(t, om, oldRoot, newRoot, start.Add(-30*time.Second))...)
		objects = append(objects, utBackupStaleLeafObjects(t, om, oldRoot, newRoot, start.Add(-90*time.Second))...)
		c := newRotationTestClient(t, objects...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.LessOrEqual(t, wait, 30*time.Second)
		assert.Greater(t, wait, 20*time.Second)
		utAssertNotReissued(t, c, om.Namespace, certspkg.AppDBManagedCertConfig(om).CertSecretName)
		utAssertNotReissued(t, c, om.Namespace, om.Name+"-managed-om-cert")
		utAssertNotReissued(t, c, om.Namespace, om.QueryableBackupCertificateSecretName())
	})

	t.Run("reissues only the components whose bundle has propagated", func(t *testing.T) {
		om := utRotationTestOM(true, true, true)
		objects := []client.Object{om}
		objects = append(objects, utAppDBStaleLeafObjects(t, om, oldRoot, newRoot, time.Now())...)
		objects = append(objects, utOMStaleLeafObjects(t, om, oldRoot, newRoot, time.Now())...)
		objects = append(objects, utBackupStaleLeafObjects(t, om, oldRoot, newRoot, propagated)...)
		c := newRotationTestClient(t, objects...)

		wait := utRotationTestReconciler(t, c).reissueManagedCertificatesOnRotatedCA(t.Context(), om, zap.S())
		assert.Positive(t, wait)
		assert.LessOrEqual(t, wait, managedCABundlePropagationDelay)
		utAssertNotReissued(t, c, om.Namespace, certspkg.AppDBManagedCertConfig(om).CertSecretName)
		utAssertNotReissued(t, c, om.Namespace, om.Name+"-managed-om-cert")
		utAssertReissued(t, c, om.Namespace, om.QueryableBackupCertificateSecretName())
	})
}
