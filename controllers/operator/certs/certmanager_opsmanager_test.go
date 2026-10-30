package certs

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	apistatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	managedTestNamespace = "test-ns"
	managedTestUID       = types.UID("om-uid-123")
)

func managedTestLogger() *zap.SugaredLogger {
	return zap.NewNop().Sugar()
}

func managedTestOM() *omv1.MongoDBOpsManager {
	return &omv1.MongoDBOpsManager{
		TypeMeta: metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDBOpsManager"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: managedTestNamespace,
			UID:       managedTestUID,
		},
		Spec: omv1.MongoDBOpsManagerSpec{
			AppDB: &omv1.AppDBSpec{
				OpsManagerName: "example",
				Namespace:      managedTestNamespace,
				ClusterDomain:  "cluster.local",
				Members:        3,
				Security: &mdbv1.Security{
					ManagedCertificate: &mdbv1.ManagedCertificate{},
					Authentication: &mdbv1.Authentication{
						Enabled: true,
						Modes:   []mdbv1.AuthMode{util.SCRAM},
					},
				},
			},
		},
	}
}

func newManagedCertTestClient(t *testing.T, objects ...client.Object) kubernetesClient.Client {
	t.Helper()
	return newManagedCertTestClientWithInterceptor(t, interceptor.Funcs{}, objects...)
}

func newManagedCertTestClientWithInterceptor(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) kubernetesClient.Client {
	t.Helper()
	scheme, err := v1.SchemeBuilder.Build()
	require.NoError(t, err)
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, certmanagerv1.AddToScheme(scheme))
	return kubernetesClient.NewClient(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(funcs).Build())
}

func serverCertificateOptions() ServerCertificateOptions {
	return ServerCertificateOptions{
		NamingBase:   "example-managed-om",
		SecretName:   "example-managed-om-cert",
		DNSNames:     []string{"example-svc.test-ns.svc.cluster.local"},
		IPAddresses:  []string{"10.0.0.10"},
		CABundleName: "example-managed-om-ca-bundle",
		CABundleKey:  "mms-ca.crt",
	}
}

func backupServerCertificateOptions() ServerCertificateOptions {
	return ServerCertificateOptions{
		NamingBase:   "example-managed-backup",
		SecretName:   "example-managed-backup-cert",
		DNSNames:     []string{"example-backup-0.example-backup-svc.test-ns.svc.cluster.local"},
		CABundleName: "example-managed-backup-ca-bundle",
		CABundleKey:  "mms-ca.crt",
	}
}

func getManagedCertificate(t *testing.T, ctx context.Context, c kubernetesClient.Client, name string) *certmanagerv1.Certificate {
	t.Helper()
	cert := &certmanagerv1.Certificate{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace}, cert))
	return cert
}

func listManagedCertificates(t *testing.T, ctx context.Context, c kubernetesClient.Client) []certmanagerv1.Certificate {
	t.Helper()
	certs := &certmanagerv1.CertificateList{}
	require.NoError(t, c.List(ctx, certs, client.InNamespace(managedTestNamespace)))
	return certs.Items
}

func listManagedIssuers(t *testing.T, ctx context.Context, c kubernetesClient.Client) []certmanagerv1.Issuer {
	t.Helper()
	issuers := &certmanagerv1.IssuerList{}
	require.NoError(t, c.List(ctx, issuers, client.InNamespace(managedTestNamespace)))
	return issuers.Items
}

func markManagedCertificatesReady(t *testing.T, ctx context.Context, c kubernetesClient.Client, mutate func(*certmanagerv1.Certificate)) {
	t.Helper()
	certs := listManagedCertificates(t, ctx, c)
	require.NotEmpty(t, certs)
	for i := range certs {
		cert := certs[i]
		cert.Status.Conditions = []certmanagerv1.CertificateCondition{{
			Type:               certmanagerv1.CertificateConditionReady,
			Status:             cmmeta.ConditionTrue,
			ObservedGeneration: cert.Generation,
		}}
		if mutate != nil {
			mutate(&cert)
		}
		require.NoError(t, c.Update(ctx, &cert))
	}
}

func assertOwnedByManagedTestOM(t *testing.T, refs []metav1.OwnerReference) {
	t.Helper()
	require.Len(t, refs, 1)
	assert.Equal(t, "example", refs[0].Name)
	assert.Equal(t, managedTestUID, refs[0].UID)
	assert.Equal(t, "MongoDBOpsManager", refs[0].Kind)
	require.NotNil(t, refs[0].Controller)
	assert.True(t, *refs[0].Controller)
}

func managedCAConfigMap(t *testing.T, ctx context.Context, c kubernetesClient.Client, name string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: managedTestNamespace}, cm))
	return cm
}

func TestManagedServerCertificate(t *testing.T) {
	duration := metav1.Duration{Duration: 24 * time.Hour}
	renewBefore := metav1.Duration{Duration: 6 * time.Hour}

	t.Run("issuer override, renewal settings, subject, SANs and IPs", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t)
		cfg := &omv1.ManagedCertificate{
			CertificateConfig: mdbv1.CertificateConfig{
				IssuerRef:   &mdbv1.IssuerRef{Name: "custom-issuer", Kind: "ClusterIssuer"},
				Duration:    &duration,
				RenewBefore: &renewBefore,
			},
			Subject: &mdbv1.X509Subject{Organizations: []string{"Example Org"}, CommonName: "om.example.com"},
		}

		status, _ := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		assert.False(t, status.IsOK(), "the certificate has not been reported ready")

		cert := getManagedCertificate(t, ctx, c, "example-managed-om-cert")
		assert.Equal(t, "example-managed-om-cert", cert.Spec.SecretName)
		assert.Equal(t, []string{"example-svc.test-ns.svc.cluster.local"}, cert.Spec.DNSNames)
		assert.Equal(t, []string{"10.0.0.10"}, cert.Spec.IPAddresses)
		assert.Equal(t, "custom-issuer", cert.Spec.IssuerRef.Name)
		assert.Equal(t, "ClusterIssuer", cert.Spec.IssuerRef.Kind)
		require.NotNil(t, cert.Spec.Duration)
		require.NotNil(t, cert.Spec.RenewBefore)
		assert.Equal(t, duration.Duration, cert.Spec.Duration.Duration)
		assert.Equal(t, renewBefore.Duration, cert.Spec.RenewBefore.Duration)
		require.NotNil(t, cert.Spec.Subject)
		assert.Equal(t, []string{"Example Org"}, cert.Spec.Subject.Organizations)
		assert.Equal(t, "om.example.com", cert.Spec.CommonName)
		assert.Equal(t, memberServerCertUsages, cert.Spec.Usages)
		assert.NotContains(t, cert.Spec.Usages, certmanagerv1.UsageClientAuth)
		assert.False(t, cert.Spec.IsCA)
		assertOwnedByManagedTestOM(t, cert.OwnerReferences)

		issuers := &certmanagerv1.IssuerList{}
		require.NoError(t, c.List(ctx, issuers, client.InNamespace(managedTestNamespace)))
		assert.Empty(t, issuers.Items, "a user issuer must not create operator-managed Issuers")
	})

	t.Run("self-signed CA chain is scoped to the naming base", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t)

		status, _ := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
		assert.False(t, status.IsOK(), "the certificates have not been reported ready")

		caCert := getManagedCertificate(t, ctx, c, "example-managed-om-server-ca")
		assert.True(t, caCert.Spec.IsCA)
		assert.Equal(t, "example-managed-om-server-ca", caCert.Spec.SecretName)
		assert.Equal(t, "example-managed-om-server-selfsigned", caCert.Spec.IssuerRef.Name)
		assertOwnedByManagedTestOM(t, caCert.OwnerReferences)

		leaf := getManagedCertificate(t, ctx, c, "example-managed-om-cert")
		assert.Equal(t, "example-managed-om-server-ca-issuer", leaf.Spec.IssuerRef.Name)
		assert.NotContains(t, leaf.Spec.Usages, certmanagerv1.UsageClientAuth)

		issuers := &certmanagerv1.IssuerList{}
		require.NoError(t, c.List(ctx, issuers, client.InNamespace(managedTestNamespace)))
		names := make([]string, 0, len(issuers.Items))
		for _, issuer := range issuers.Items {
			names = append(names, issuer.Name)
			assertOwnedByManagedTestOM(t, issuer.OwnerReferences)
		}
		assert.ElementsMatch(t, []string{"example-managed-om-server-selfsigned", "example-managed-om-server-ca-issuer"}, names)
	})

	t.Run("CA bundle prefers the issuer emitted ca.crt", func(t *testing.T) {
		ctx := context.Background()
		fallback := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "fallback-ca", Namespace: managedTestNamespace},
			Data:       map[string]string{"ca.crt": "FALLBACK-CA"},
		}
		c := newManagedCertTestClient(t, fallback)
		cfg := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
			IssuerRef: &mdbv1.IssuerRef{Name: "custom-issuer"},
			CA:        &mdbv1.CARef{Name: "fallback-ca", Kind: "ConfigMap", Key: "ca.crt"},
		}}

		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-cert", Namespace: managedTestNamespace},
			Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte("EMITTED-CA")},
		}))

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		assert.True(t, status.IsOK())
		assert.Equal(t, metav1.ConditionTrue, condition.Status)

		bundle := managedCAConfigMap(t, ctx, c, "example-managed-om-ca-bundle")
		assert.Contains(t, bundle.Data["mms-ca.crt"], "EMITTED-CA")
		assert.NotContains(t, bundle.Data["mms-ca.crt"], "FALLBACK-CA")
		assertOwnedByManagedTestOM(t, bundle.OwnerReferences)
	})

	t.Run("CA bundle falls back to the configured CA and appends additional trusted CAs", func(t *testing.T) {
		ctx := context.Background()
		fallback := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "fallback-ca", Namespace: managedTestNamespace},
			Data:       map[string]string{"ca.crt": "FALLBACK-CA"},
		}
		extra := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "extra-ca", Namespace: managedTestNamespace},
			Data:       map[string]string{"ca.crt": "EXTRA-CA"},
		}
		c := newManagedCertTestClient(t, fallback, extra)
		cfg := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
			IssuerRef:            &mdbv1.IssuerRef{Name: "custom-issuer"},
			CA:                   &mdbv1.CARef{Name: "fallback-ca", Kind: "ConfigMap", Key: "ca.crt"},
			AdditionalTrustedCAs: []mdbv1.CARef{{Name: "extra-ca", Kind: "ConfigMap", Key: "ca.crt"}},
		}}

		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-cert", Namespace: managedTestNamespace},
		}))

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		assert.True(t, status.IsOK())
		assert.Equal(t, metav1.ConditionTrue, condition.Status)

		bundle := managedCAConfigMap(t, ctx, c, "example-managed-om-ca-bundle")
		assert.Equal(t, "FALLBACK-CA\nEXTRA-CA\n", bundle.Data["mms-ca.crt"])
	})

	t.Run("missing required CA input fails with an actionable message", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t)
		cfg := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
			IssuerRef: &mdbv1.IssuerRef{Name: "custom-issuer"},
		}}

		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-cert", Namespace: managedTestNamespace},
		}))

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		assert.False(t, status.IsOK())
		assert.Equal(t, apistatus.PhaseFailed, status.Phase())
		assert.Contains(t, status.Message(), "did not populate ca.crt")
		assert.Contains(t, status.Message(), "security.managedCertificate")
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, reasonCertificateIssuanceError, condition.Reason)
	})
}

func TestManagedServerCertificateReadiness(t *testing.T) {
	fallback := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "fallback-ca", Namespace: managedTestNamespace},
			Data:       map[string]string{"ca.crt": "FALLBACK-CA"},
		}
	}
	cfgWithFallback := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
		IssuerRef: &mdbv1.IssuerRef{Name: "custom-issuer"},
		CA:        &mdbv1.CARef{Name: "fallback-ca", Kind: "ConfigMap", Key: "ca.crt"},
	}}

	t.Run("fresh ready condition completes", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t, fallback())
		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfgWithFallback, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-cert", Namespace: managedTestNamespace},
		}))

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfgWithFallback, serverCertificateOptions(), managedTestLogger())
		assert.True(t, status.IsOK())
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		assert.Equal(t, reasonAllCertificatesReady, condition.Reason)
	})

	t.Run("stale ready condition does not pass the generation check", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t, fallback())
		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfgWithFallback, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, func(cert *certmanagerv1.Certificate) {
			cert.Generation = 4
		})

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfgWithFallback, serverCertificateOptions(), managedTestLogger())
		assert.Equal(t, apistatus.PhasePending, status.Phase())
		assert.Contains(t, status.Message(), "example-managed-om-cert")
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, reasonCertificatesIssuing, condition.Reason)
	})

	t.Run("missing output Secret remains pending", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t)
		cfg := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
			IssuerRef: &mdbv1.IssuerRef{Name: "custom-issuer"},
		}}
		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)

		status, _ := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, serverCertificateOptions(), managedTestLogger())
		assert.Equal(t, apistatus.PhasePending, status.Phase())
		assert.Contains(t, status.Message(), "Waiting for cert-manager to write the certificate secret example-managed-om-cert")
	})

	t.Run("self-signed leaf without its output Secret remains pending", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t)
		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)
		// The self-signed CA Secret alone is enough to resolve the trust bundle, but not
		// enough to prove that the leaf certificate material has been written.
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-server-ca", Namespace: managedTestNamespace},
			Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte("CA")},
		}))

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
		assert.Equal(t, apistatus.PhasePending, status.Phase())
		assert.Contains(t, status.Message(), "Waiting for cert-manager to write the certificate secret example-managed-om-cert")
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, reasonCertificatesIssuing, condition.Reason)
	})

	t.Run("output Secret read errors propagate", func(t *testing.T) {
		ctx := context.Background()
		failLeafSecretRead := false
		leafSecretReadErr := errors.New("leaf secret read denied")
		c := newManagedCertTestClientWithInterceptor(t, interceptor.Funcs{
			Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Secret); ok && failLeafSecretRead && key.Name == "example-managed-om-cert" {
					return leafSecretReadErr
				}
				return inner.Get(ctx, key, obj, opts...)
			},
		})
		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
		markManagedCertificatesReady(t, ctx, c, nil)
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-server-ca", Namespace: managedTestNamespace},
			Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte("CA")},
		}))

		failLeafSecretRead = true
		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
		assert.Equal(t, apistatus.PhaseFailed, status.Phase())
		assert.Contains(t, status.Message(), "leaf secret read denied")
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, reasonCertificateIssuanceError, condition.Reason)
	})

	t.Run("renewal failure keeps the workflow OK and reports a false condition", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t, fallback())
		_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfgWithFallback, serverCertificateOptions(), managedTestLogger())
		lastFailure := metav1.NewTime(time.Now().Add(-time.Hour))
		markManagedCertificatesReady(t, ctx, c, func(cert *certmanagerv1.Certificate) {
			cert.Status.LastFailureTime = &lastFailure
		})
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-cert", Namespace: managedTestNamespace},
		}))

		status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfgWithFallback, serverCertificateOptions(), managedTestLogger())
		assert.True(t, status.IsOK())
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, reasonCertificateRenewalFailed, condition.Reason)
		assert.Contains(t, condition.Message, "renewal is failing")
	})

	t.Run("independent scopes are issued even when another scope is pending", func(t *testing.T) {
		ctx := context.Background()
		c := newManagedCertTestClient(t)

		omStatus, _ := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, serverCertificateOptions(), managedTestLogger())
		assert.Equal(t, apistatus.PhasePending, omStatus.Phase())

		backupStatus, _ := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), &omv1.ManagedCertificate{}, backupServerCertificateOptions(), managedTestLogger())
		assert.Equal(t, apistatus.PhasePending, backupStatus.Phase())

		backupLeaf := getManagedCertificate(t, ctx, c, "example-managed-backup-cert")
		assert.Equal(t, "example-managed-backup-server-ca-issuer", backupLeaf.Spec.IssuerRef.Name)
		assert.Contains(t, backupLeaf.Spec.SecretName, "example-managed-backup")
		assertOwnedByManagedTestOM(t, backupLeaf.OwnerReferences)
	})
}

// TestManagedServerCertificateWriteGenerationReadiness reproduces a cached read that lags the
// Certificate write made during the same preparation pass. The Ready status for the previous
// generation must not be accepted just because the cached object still reports
// observedGeneration >= cached generation.
func TestManagedServerCertificateWriteGenerationReadiness(t *testing.T) {
	ctx := context.Background()
	fallback := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "fallback-ca", Namespace: managedTestNamespace},
		Data:       map[string]string{"ca.crt": "FALLBACK-CA"},
	}
	cfg := &omv1.ManagedCertificate{CertificateConfig: mdbv1.CertificateConfig{
		IssuerRef: &mdbv1.IssuerRef{Name: "custom-issuer"},
		CA:        &mdbv1.CARef{Name: "fallback-ca", Kind: "ConfigMap", Key: "ca.crt"},
	}}

	var cachedLeaf *certmanagerv1.Certificate
	c := newManagedCertTestClientWithInterceptor(t, interceptor.Funcs{
		Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if cert, ok := obj.(*certmanagerv1.Certificate); ok && cachedLeaf != nil && key.Name == cachedLeaf.Name {
				cachedLeaf.DeepCopyInto(cert)
				return nil
			}
			return inner.Get(ctx, key, obj, opts...)
		},
		Update: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cert, ok := obj.(*certmanagerv1.Certificate); ok {
				persisted := &certmanagerv1.Certificate{}
				if err := inner.Get(ctx, client.ObjectKeyFromObject(cert), persisted); err != nil {
					return err
				}
				if !reflect.DeepEqual(persisted.Spec, cert.Spec) {
					cert.Generation = persisted.Generation + 1
				}
			}
			return inner.Update(ctx, obj, opts...)
		},
	}, fallback)

	opts := serverCertificateOptions()
	opts.DNSNames = []string{"old.example.com"}
	_, _ = EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, opts, managedTestLogger())

	leaf := getManagedCertificate(t, ctx, c, "example-managed-om-cert")
	leaf.Generation = 1
	leaf.Status.Conditions = []certmanagerv1.CertificateCondition{{
		Type:               certmanagerv1.CertificateConditionReady,
		Status:             cmmeta.ConditionTrue,
		ObservedGeneration: 1,
	}}
	require.NoError(t, c.Update(ctx, leaf))
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "example-managed-om-cert", Namespace: managedTestNamespace},
		Data:       map[string][]byte{corev1.ServiceAccountRootCAKey: []byte("OLD-CA")},
	}))

	// The informer cache still holds the generation-1 object while the write below bumps the
	// Certificate to generation 2 with the new SAN.
	cachedLeaf = leaf.DeepCopy()
	opts.DNSNames = []string{"new.example.com"}
	status, condition := EnsureServerCertificateAndCA(ctx, c, managedTestOM(), cfg, opts, managedTestLogger())
	cachedLeaf = nil

	persisted := getManagedCertificate(t, ctx, c, "example-managed-om-cert")
	assert.Equal(t, int64(2), persisted.Generation)
	require.Len(t, persisted.Status.Conditions, 1)
	assert.Equal(t, int64(1), persisted.Status.Conditions[0].ObservedGeneration)
	assert.Equal(t, apistatus.PhasePending, status.Phase(), "cached Ready for the previous generation must not satisfy an updated desired Certificate")
	assert.Contains(t, status.Message(), "example-managed-om-cert")
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, reasonCertificatesIssuing, condition.Reason)

	bundle := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Name: "example-managed-om-ca-bundle", Namespace: managedTestNamespace}, bundle)
	assert.True(t, apiErrors.IsNotFound(err), "the CA bundle must not be published before the newly written generation is ready")
}
