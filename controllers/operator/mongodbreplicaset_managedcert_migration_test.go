package operator

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	envtestenv "github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

const migrationResourceName = "migrate-rs"

// TestReplicaSetMigratesFromLegacyTLSSecretsToManagedCertificates walks the migration from
// user-provided TLS certificates to spec.security.managedCertificate against a real API server.
//
// The deployment starts the legacy way: TLS is enabled without certsSecretPrefix, so the operator
// expects the user's certificates at the default secret names (<name>-cert, <name>-clusterfile) and
// the CA at the configured ConfigMap. It is then migrated in one edit: tls.ca goes away and
// managedCertificate turns on, which is the only edit the API accepts (the two are mutually
// exclusive). The operator waits for cert-manager, leaving the running deployment on the user's
// certificates while it does, and switches it to the operator's certificates once they are issued.
// The user's secrets are never touched.
func TestReplicaSetMigratesFromLegacyTLSSecretsToManagedCertificates(t *testing.T) {
	ctx := context.Background()
	c := envtestenv.Shared(t).Client
	// The control plane is shared by the whole package, so the test takes its own namespace: runs
	// with -count>1 must not see each other.
	namespace := fmt.Sprintf("migrate-%d", time.Now().UnixNano())
	resourceKey := kube.ObjectKey(namespace, migrationResourceName)

	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	for _, obj := range mock.GetDefaultResources() {
		obj.SetNamespace(namespace)
		require.NoError(t, c.Create(ctx, obj))
	}

	createLegacyTLSSecrets(ctx, t, c, namespace)

	rs := DefaultReplicaSetBuilder().
		SetName(migrationResourceName).
		EnableSCRAM().
		EnableX509().
		AgentAuthMode(util.SCRAM).
		EnableX509InternalClusterAuth().
		EnableTLS().
		SetTLSCA(migrationResourceName + "-ca").
		Build()
	rs.Namespace = namespace
	require.NoError(t, c.Create(ctx, rs))

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	// The operator decides cert-manager readiness once at startup; here the CRDs are installed and
	// the API server allows everything, so the reconciler is built ready.
	reconciler := newReplicaSetReconciler(ctx, searchIndexedClient{c}, nil, "fake-init-database-image", "fake-database-image",
		false, false, false, "", architectures.NonStatic, omConnectionFactory.GetConnectionFunc, true)

	reconcileReplicaSet := func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
		require.NoError(t, err)
	}

	// reconcileUntilRunning reconciles until the resource reaches Running, marking the StatefulSet
	// ready between passes: envtest has no kubelet or StatefulSet controller, and the pass that
	// changes the StatefulSet ends Pending because nothing has rolled the pods yet.
	reconcileUntilRunning := func() {
		for i := 0; i < 5; i++ {
			reconcileReplicaSet()
			markStatefulSetsReady(ctx, t, c, namespace, omConnectionFactory)
			require.NoError(t, c.Get(ctx, resourceKey, rs))
			if rs.Status.Phase == status.PhaseRunning {
				return
			}
		}
		t.Fatalf("the resource did not reach Running, last phase %q: %s", rs.Status.Phase, rs.Status.Message)
	}

	// Legacy: the deployment has to be healthy before the migration.
	reconcileUntilRunning()

	legacySts := statefulSetForResource(ctx, t, c, resourceKey)
	assert.Equal(t, migrationResourceName+"-cert-pem", certificateVolumeSecretName(t, legacySts))
	assert.Equal(t, migrationResourceName+"-ca", caVolumeConfigMapName(t, legacySts))

	// Migrate. tls.ca and managedCertificate are mutually exclusive, so the CA has to go in the
	// same update that turns the feature on.
	rs.Spec.Security.TLSConfig.CA = ""
	rs.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	require.NoError(t, c.Update(ctx, rs))

	reconcileReplicaSet()

	// The operator is waiting for cert-manager: its Certificate objects exist, but the deployment
	// keeps running on the user's certificates.
	require.NoError(t, c.Get(ctx, resourceKey, rs))
	assert.Equal(t, status.PhasePending, rs.Status.Phase)

	managedCert := &certmanagerv1.Certificate{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(namespace, migrationResourceName+"-managed-cert"), managedCert))
	assert.Equal(t, migrationResourceName+"-managed-cert", managedCert.Spec.SecretName)

	pendingSts := statefulSetForResource(ctx, t, c, resourceKey)
	assert.Equal(t, certificateVolumeSecretName(t, legacySts), certificateVolumeSecretName(t, pendingSts),
		"the deployment keeps the user's certificate while cert-manager issues")
	assertSecretExists(ctx, t, c, namespace, migrationResourceName+"-cert")
	assertSecretExists(ctx, t, c, namespace, migrationResourceName+"-clusterfile")

	// There is no cert-manager controller in envtest, so the test plays it: every Certificate gets
	// its secret and a Ready condition, which is all the operator waits for.
	fakeCertManagerIssueAllCertificates(ctx, t, c, namespace)

	// The deployment has to come back to Running: the pass that reissues the StatefulSet with the
	// managed volumes ends Pending until the rollout is simulated, and the next pass has to finish
	// the migration without any errors.
	reconcileUntilRunning()

	migratedSts := statefulSetForResource(ctx, t, c, resourceKey)
	assert.Equal(t, migrationResourceName+"-managed-cert-pem", certificateVolumeSecretName(t, migratedSts),
		"the deployment mounts the operator's certificate after the migration")
	assert.Equal(t, migrationResourceName+"-managed-server-ca-bundle", caVolumeConfigMapName(t, migratedSts),
		"the deployment trusts the operator's CA bundle after the migration")

	// The user's secrets are left in place, unused: the operator never deletes what it did not create.
	assertSecretExists(ctx, t, c, namespace, migrationResourceName+"-cert")
	assertSecretExists(ctx, t, c, namespace, migrationResourceName+"-clusterfile")
}

// searchIndexedClient serves the MongoDBSearch lookup the controller expects by field index. The
// manager registers searchv1.MongoDBSearchIndexFieldName on its cache, which a plain client cannot
// do, so this client answers that one list itself, filtering exactly like the index function does.
type searchIndexedClient struct {
	client.Client
}

func (c searchIndexedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	searchList, ok := list.(*searchv1.MongoDBSearchList)
	if !ok {
		return c.Client.List(ctx, list, opts...)
	}

	listOptions := &client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(listOptions)
	}
	if listOptions.FieldSelector == nil {
		return c.Client.List(ctx, list, opts...)
	}
	requirements := listOptions.FieldSelector.Requirements()
	if len(requirements) != 1 || requirements[0].Field != searchv1.MongoDBSearchIndexFieldName {
		return c.Client.List(ctx, list, opts...)
	}

	allSearches := &searchv1.MongoDBSearchList{}
	if err := c.Client.List(ctx, allSearches); err != nil {
		return err
	}
	for _, search := range allSearches.Items {
		ref := search.GetMongoDBResourceRef()
		if ref != nil && ref.Namespace+"/"+ref.Name == requirements[0].Value {
			searchList.Items = append(searchList.Items, search)
		}
	}
	return nil
}

// createLegacyTLSSecrets creates what a user supplies for a TLS deployment without
// certsSecretPrefix: the member certificate, the internal cluster certificate, and the CA ConfigMap.
func createLegacyTLSSecrets(ctx context.Context, t *testing.T, c client.Client, namespace string) {
	certPEM, keyPEM := createMockCertAndKeyBytes()
	for _, name := range []string{migrationResourceName + "-cert", migrationResourceName + "-clusterfile"} {
		require.NoError(t, c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
		}))
	}
	require.NoError(t, c.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: migrationResourceName + "-ca", Namespace: namespace},
		Data:       map[string]string{tls.CAConfigMapKey: string(certPEM)},
	}))
}

func statefulSetForResource(ctx context.Context, t *testing.T, c client.Client, key client.ObjectKey) appsv1.StatefulSet {
	sts := appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, key, &sts))
	return sts
}

// markStatefulSetsReady reports every StatefulSet in the namespace as fully rolled out, the way the
// StatefulSet controller and the kubelet would: observedGeneration catches up with the current
// generation and every replica is ready. It also registers the pod hostnames as automation agents
// with the mocked Ops Manager: nothing runs the pods, so nothing registers them, and the reconcile
// fails waiting for agents that can never appear.
func markStatefulSetsReady(ctx context.Context, t *testing.T, c client.Client, namespace string, omConnectionFactory *om.CachedOMConnectionFactory) {
	stsList := appsv1.StatefulSetList{}
	require.NoError(t, c.List(ctx, &stsList, client.InNamespace(namespace)))
	for i := range stsList.Items {
		sts := &stsList.Items[i]
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.UpdatedReplicas = *sts.Spec.Replicas
		sts.Status.ReadyReplicas = *sts.Spec.Replicas
		sts.Status.Replicas = *sts.Spec.Replicas
		require.NoError(t, c.Status().Update(ctx, sts))

		if mocked, ok := omConnectionFactory.GetConnectionForResource(sts).(*om.MockedOmConnection); ok {
			hostnames, _ := dns.GetDnsForStatefulSet(*sts, "", nil)
			mocked.AddHosts(hostnames)
		}
	}
}

// certificateVolumeSecretName returns the Secret the deployment mounts for its TLS certificate.
func certificateVolumeSecretName(t *testing.T, sts appsv1.StatefulSet) string {
	t.Helper()
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name == util.SecretVolumeName {
			return volume.Secret.SecretName
		}
	}
	t.Fatalf("statefulset %s has no %s volume", sts.Name, util.SecretVolumeName)
	return ""
}

// caVolumeConfigMapName returns the ConfigMap the deployment mounts for its CA.
func caVolumeConfigMapName(t *testing.T, sts appsv1.StatefulSet) string {
	t.Helper()
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name == tls.ConfigMapVolumeCAName {
			return volume.ConfigMap.Name
		}
	}
	t.Fatalf("statefulset %s has no %s volume", sts.Name, tls.ConfigMapVolumeCAName)
	return ""
}

func assertSecretExists(ctx context.Context, t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	secret := corev1.Secret{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(namespace, name), &secret), "secret %s must still exist", name)
}

// fakeCertManagerIssueAllCertificates plays cert-manager for the tests: for every Certificate that
// is not ready yet it writes the secret the Certificate asks for and marks the Certificate ready,
// which is all the operator waits for. The certificates are real x509, so the operator's checks can
// parse them.
func fakeCertManagerIssueAllCertificates(ctx context.Context, t *testing.T, c client.Client, namespace string) {
	certificates := &certmanagerv1.CertificateList{}
	require.NoError(t, c.List(ctx, certificates, client.InNamespace(namespace)))
	require.NotEmpty(t, certificates.Items)

	for i := range certificates.Items {
		cert := &certificates.Items[i]
		if certificateIsReady(cert) {
			continue
		}

		certPEM, keyPEM := createMockCertAndKeyBytes(func(template *x509.Certificate) {
			template.Subject = pkix.Name{CommonName: cert.Spec.CommonName}
			template.DNSNames = cert.Spec.DNSNames
			template.IsCA = cert.Spec.IsCA
			if cert.Spec.Subject != nil {
				template.Subject.Organization = cert.Spec.Subject.Organizations
				template.Subject.OrganizationalUnit = cert.Spec.Subject.OrganizationalUnits
				template.Subject.Country = cert.Spec.Subject.Countries
			}
		})
		data := map[string][]byte{
			corev1.TLSCertKey:              certPEM,
			corev1.TLSPrivateKeyKey:        keyPEM,
			corev1.ServiceAccountRootCAKey: certPEM,
		}

		existing := corev1.Secret{}
		err := c.Get(ctx, kube.ObjectKey(namespace, cert.Spec.SecretName), &existing)
		switch {
		case err == nil:
			existing.Data = data
			existing.Type = corev1.SecretTypeTLS
			require.NoError(t, c.Update(ctx, &existing))
		case apiErrors.IsNotFound(err):
			require.NoError(t, c.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: cert.Spec.SecretName, Namespace: namespace},
				Type:       corev1.SecretTypeTLS,
				Data:       data,
			}))
		default:
			require.NoError(t, err)
		}

		cert.Status.Conditions = []certmanagerv1.CertificateCondition{{
			Type:               certmanagerv1.CertificateConditionReady,
			Status:             cmmeta.ConditionTrue,
			Reason:             "Ready",
			ObservedGeneration: cert.Generation,
		}}
		require.NoError(t, c.Status().Update(ctx, cert))
	}
}

func certificateIsReady(cert *certmanagerv1.Certificate) bool {
	for _, condition := range cert.Status.Conditions {
		if condition.Type == certmanagerv1.CertificateConditionReady {
			return condition.Status == cmmeta.ConditionTrue && condition.ObservedGeneration == cert.Generation
		}
	}
	return false
}
