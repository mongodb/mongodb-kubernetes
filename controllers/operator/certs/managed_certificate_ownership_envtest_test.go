package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"

	mongodbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMain starts the control plane the tests in this package share. The ownership test below needs a
// real API server, because what it checks is what the API server stores on the secrets the operator
// writes, owner references and their UID included.
//
// No CRDs are installed: the test builds the resources the operator manages in memory and drives the
// code that writes the secrets, not the controllers.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs()))
}

const (
	// managedCertTestNamespace is the namespace the test writes into. Every topology uses the same
	// one, with distinct resource names, so the secrets of one topology cannot collide with another's.
	managedCertTestNamespace = "default"

	// managedCertTestUID stands in for the UID the API server assigns a resource, which is part of
	// every owner reference that points at it.
	managedCertTestUID = types.UID("11111111-1111-1111-1111-111111111111")
)

// TestManagedCertificateObjectsAreCleanedUp covers, for every topology that supports operator-managed
// certificates, that the secrets and CA bundle ConfigMaps the operator creates for a resource are
// removed when the resource is deleted. One of two routes removes each object:
//
//   - By owner (assertRemovedByOwner): the object is in the cluster that holds the resource and has an
//     owner reference to it, so Kubernetes deletes it together with the resource.
//   - By labels (assertRemovedByLabels): the object is in a member cluster. Kubernetes cannot follow an
//     owner reference into another cluster, and deletes an object whose owner it cannot find, so the
//     object must have no owner reference; it carries the labels the operator's cleanup on deletion
//     looks for instead.
//
// An object covered by neither outlives the resource. For a secret that matters beyond the leak: the
// next resource with the same name gets the old certificate back, because cert-manager compares a
// Certificate with its secret by name only.
//
// There is one API server, which plays the cluster that holds the resource. For the multi-cluster
// topologies the test writes the -pem secrets with the options the controller uses for a member
// cluster, and checks what lands on them, not which cluster they land in. The CA bundles are copied to
// a separate fake client, because the copy has the same name as the original.
func TestManagedCertificateObjectsAreCleanedUp(t *testing.T) {
	ctx := context.Background()
	log := zap.S()

	c := newManagedCertTestClient(t)
	sc := secrets.SecretClient{KubeClient: c}

	for _, tc := range managedCertTopologies(sc) {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.resource
			security := res.GetSecurity()
			require.True(t, security.IsManagedCertificateEnabled(), "the fixture has to use operator-managed certificates")
			require.True(t, security.RequiresX509ClientCerts(), "the fixture has to create every kind of certificate the feature manages")

			// Nothing is deleted here: no garbage collector and no operator run against this API server.
			// Each step writes objects with the real operator code and checks that they are set up to be
			// removed with the resource, by owner or by labels. The E2Es check that they are then deleted.

			// 1. The leaf certificates: member, clusterfile (one of each per component) and agent.
			//    Build the Certificate the operator would create for each, check that it asks cert-manager
			//    to put the resource's labels on its secret, and then play cert-manager: write the secret
			//    it would issue the certificate into, with those labels and no owner.
			issuedNames := make([]string, 0)
			for _, opts := range tc.issuedOptions {
				for _, cert := range componentCertificates(ctx, c, res, opts) {
					assertCertSpecCarriesOwnerLabels(t, res, cert)
					createCertManagerSecret(t, c, cert.certName, cert.spec.SecretTemplate.Labels)
					issuedNames = append(issuedNames, cert.certName)
				}
			}
			agentCert, ok := agentCertificate(res)
			require.True(t, ok, "the fixture has to require an agent certificate")
			assertCertSpecCarriesOwnerLabels(t, res, agentCert)
			createCertManagerSecret(t, c, agentCert.certName, agentCert.spec.SecretTemplate.Labels)
			issuedNames = append(issuedNames, agentCert.certName)

			// 2. The two self-signed CAs, server and client. Play cert-manager again and write their
			//    secrets, with the labels the operator asks for: cert-manager can issue a CA secret
			//    again while the resource is being deleted, and the copy it writes then has no owner,
			//    so the cleanup has to find it by label like the other secrets.
			caNames := []string{managedCACertName(res, categoryServer), managedCACertName(res, categoryClient)}
			for _, name := range caNames {
				createCertManagerSecret(t, c, name, res.GetOwnerLabels())
			}

			// 3. The operator gives every secret cert-manager wrote (steps 1 and 2) an owner reference to
			//    the resource. Check each one now has it, so it is removed by owner.
			names := append(caNames, issuedNames...)
			require.True(t, ensureIssuedSecretsAreOwned(ctx, c, res, names).IsOK())
			for _, name := range names {
				s := getSecret(t, c, name)
				assertRemovedByOwner(t, &s, ownerRefsForIssuedSecrets(res))
			}

			// 4. The CA bundle ConfigMaps the pods mount, server and client.
			//    4a. The operator writes them in the cluster that holds the resource: removed by owner.
			bundleNames := []string{ManagedServerCABundleConfigMapName(res.GetName()), ManagedClientCABundleConfigMapName(res.GetName())}
			status := EnsureCAConfigMapFromIssuedSecret(ctx, c, res, tc.issuedOptions[0], security.RequiresX509ClientCerts(), log)
			require.True(t, status.IsOK(), "writing the CA bundles: %s", status.Message())
			for _, name := range bundleNames {
				cm := getConfigMap(t, c, name)
				assertRemovedByOwner(t, &cm, kube.BaseOwnerReference(res))
			}
			//    4b. For a multi-cluster deployment the operator copies them to every member cluster: removed
			//        by labels. The copy goes to a separate fake client standing in for a member cluster,
			//        because it has the same name as the original.
			if tc.multiCluster {
				member, _ := mock.NewDefaultFakeClient()
				targets := []MemberClusterTarget{{Name: "test-cluster-0", Client: member}}
				status := ReplicateManagedCABundlesToMemberClusters(ctx, c, res.GetNamespace(), bundleNames, targets, res.GetOwnerLabels(), log)
				require.True(t, status.IsOK(), "copying the CA bundles to a member cluster: %s", status.Message())
				for _, name := range bundleNames {
					cm := getConfigMap(t, member, name)
					assertRemovedByLabels(t, &cm, res)
				}
			}

			// 5. The -pem secrets the pods mount, which the operator writes itself from the secrets of
			//    step 1: one per member certificate, one per clusterfile certificate, and the agent's.
			//    5a. Write them with the same options and owner the controller uses.
			pemNames := make([]string, 0)
			for _, opts := range tc.pemOptions {
				status := EnsureSSLCertsForStatefulSet(ctx, sc, sc, *security, opts, log)
				require.True(t, status.IsOK(), "writing the -pem secret of %s: %s", opts.CertSecretName, status.Message())
				pemNames = append(pemNames, opts.CertSecretName+OperatorGeneratedCertSuffix)

				if security.GetInternalClusterAuthenticationMode() == util.X509 && opts.InternalClusterSecretName != "" {
					require.NoError(t, VerifyAndEnsureCertificatesForStatefulSet(ctx, sc, sc, opts.InternalClusterSecretName, opts, log))
					pemNames = append(pemNames, opts.InternalClusterSecretName+OperatorGeneratedCertSuffix)
				}
			}
			agentSecretName := security.AgentClientCertificateSecretName(res.GetName())
			require.NoError(t, VerifyAndEnsureClientCertificatesForAgentsAndTLSType(
				ctx, sc, sc, kube.ObjectKey(res.GetNamespace(), agentSecretName),
				tc.configurator.GetOwnerReferences(), tc.configurator.GetOwnerLabels(), log))
			pemNames = append(pemNames, agentSecretName+OperatorGeneratedCertSuffix)

			//    5b. Single-cluster: they are in the cluster that holds the resource, so removed by owner.
			//        Multi-cluster: they are in the member clusters, so removed by labels. There is one API
			//        server, so this checks what the operator puts on them, not which cluster they land in.
			for _, name := range pemNames {
				s := getSecret(t, c, name)
				if tc.multiCluster {
					assertRemovedByLabels(t, &s, res)
				} else {
					assertRemovedByOwner(t, &s, kube.BaseOwnerReference(res))
				}
			}
		})
	}
}

// TestManagedCertificateIssuedSecretKeepsCertManagerOwner covers cert-manager running with
// --enable-certificate-owner-ref, which makes the Certificate the controller of the secret it writes.
// Kubernetes allows only one controller per object, so the operator's owner reference must not be one:
// otherwise the API server rejects the secret, whichever of the two writes it second.
func TestManagedCertificateIssuedSecretKeepsCertManagerOwner(t *testing.T) {
	ctx := context.Background()
	c := newManagedCertTestClient(t)
	res := managedCertTopologies(secrets.SecretClient{KubeClient: c})[0].resource

	certificate := &certmanagerv1.Certificate{ObjectMeta: metav1.ObjectMeta{
		Name: "cert-manager-owned",
		UID:  types.UID("22222222-2222-2222-2222-222222222222"),
	}}
	certManagerRef := *metav1.NewControllerRef(certificate, certmanagerv1.SchemeGroupVersion.WithKind("Certificate"))

	t.Run("cert-manager writes the secret first", func(t *testing.T) {
		require.NoError(t, c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:            "cert-manager-first",
			Namespace:       managedCertTestNamespace,
			OwnerReferences: []metav1.OwnerReference{certManagerRef},
		}}))

		status := ensureIssuedSecretsAreOwned(ctx, c, res, []string{"cert-manager-first"})
		require.True(t, status.IsOK(), status.Message())

		s := getSecret(t, c, "cert-manager-first")
		assert.True(t, secret.HasOwnerReferences(s, []metav1.OwnerReference{certManagerRef}), "cert-manager's owner has to stay")
		assertRemovedByOwner(t, &s, ownerRefsForIssuedSecrets(res))
	})

	t.Run("the operator owns the secret first", func(t *testing.T) {
		require.NoError(t, c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "operator-first",
			Namespace: managedCertTestNamespace,
		}}))
		require.True(t, ensureIssuedSecretsAreOwned(ctx, c, res, []string{"operator-first"}).IsOK())

		// cert-manager's next write adds its own owner reference.
		s := getSecret(t, c, "operator-first")
		s.OwnerReferences = append(s.OwnerReferences, certManagerRef)
		require.NoError(t, c.Update(ctx, &s), "cert-manager has to be able to make the Certificate the controller of its secret")
	})
}

// newManagedCertTestClient returns a client of the shared control plane that knows the types the
// tests in this file write.
func newManagedCertTestClient(t *testing.T) kubernetesClient.Client {
	t.Helper()
	scheme := kruntime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mongodbv1.AddToScheme(scheme))
	require.NoError(t, certmanagerv1.AddToScheme(scheme))
	crClient, err := client.New(env.Shared(t).Config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	return kubernetesClient.NewClient(crClient)
}

// managedCertTopology is one deployment topology the feature supports, with the resource it manages
// and the certificate options the operator builds for it.
type managedCertTopology struct {
	name string
	// resource is the deployment the certificates are issued for. It is built in memory, with a UID,
	// the way it looks when the operator reads it back from the API server.
	resource CertificateOwner
	// issuedOptions are the per-component options EnsureCertificatesAndCA issues the cert-manager
	// Certificates from: one for a replica set, one per component (mongos, config server, each shard)
	// for a sharded cluster.
	issuedOptions []Options
	// pemOptions are the options the operator's writers use to write the -pem secrets the pods mount.
	// For a single-cluster deployment they are the ones above; for a multi-cluster one they are the
	// per-member-cluster ones, and every secret they write lands in a member cluster.
	pemOptions []Options
	// configurator is what the controller passes to the shared certificate code, which is how the
	// agent's -pem secret is written. It is where the operator decides whether a secret may point at
	// the resource.
	configurator X509CertConfigurator
	// multiCluster is true when the pods run in member clusters other than the one that holds the
	// resource, so the -pem secrets cannot carry an owner reference to it.
	multiCluster bool
}

// managedCertTopologies returns the four topologies that implement operator-managed certificates, each
// with the options its controller builds. The security section is shared: x509 internal cluster
// authentication with an x509 agent, which is the configuration that asks for every certificate the
// feature manages (member, clusterFile and agent certs, and both CAs).
func managedCertTopologies(sc secrets.SecretClient) []managedCertTopology {
	security := managedCertSecurity()

	rs := mdbv1.NewReplicaSetBuilder().
		SetName("rs-managed-cert").
		SetNamespace(managedCertTestNamespace).
		SetMembers(3).
		SetSecurity(security).
		Build()
	rs.SetUID(managedCertTestUID)
	rsOptions := []Options{ReplicaSetConfig(*rs)}

	sharded := mdbv1.NewDefaultShardedClusterBuilder().
		SetName("sc-managed-cert").
		SetNamespace(managedCertTestNamespace).
		SetShardCountSpec(1).
		SetSecurity(security).
		Build()
	sharded.SetUID(managedCertTestUID)
	shardedOptions := []Options{
		MongosConfig(*sharded, nil, fakeScaler{current: 1, desired: 1}),
		ConfigSrvConfig(*sharded, nil, fakeScaler{current: 3, desired: 3}),
		ShardConfig(*sharded, 0, nil, fakeScaler{current: 3, desired: 3}),
	}

	mrs := multiClusterReplicaSet("mrs-managed-cert", security)
	mrsClusters := []MemberCluster{
		{ClusterNum: 0, ClusterName: "test-cluster-0", Members: 3},
		{ClusterNum: 1, ClusterName: "test-cluster-1", Members: 3},
	}

	mcSharded := mdbv1.NewDefaultMultiShardedClusterBuilder().
		SetName("mcsc-managed-cert").
		SetNamespace(managedCertTestNamespace).
		SetShardCountSpec(1).
		SetSecurity(security).
		Build()
	mcSharded.SetUID(managedCertTestUID)
	mcShardedClusters := []MemberCluster{
		{ClusterNum: 0, ClusterName: "test-cluster-0", Members: 3},
		{ClusterNum: 1, ClusterName: "test-cluster-1", Members: 3},
	}

	return []managedCertTopology{
		{
			name:          "replica-set",
			resource:      rs,
			issuedOptions: rsOptions,
			pemOptions:    rsOptions,
			configurator:  ReplicaSetX509CertConfigurator{MongoDB: rs, SecretClient: sc},
		},
		{
			name:          "sharded-cluster",
			resource:      sharded,
			issuedOptions: shardedOptions,
			pemOptions:    shardedOptions,
			// A single-cluster sharded cluster runs on the legacy central member cluster, which is
			// the cluster that holds the resource.
			configurator: ShardedSetX509CertConfigurator{
				MongoDB:          sharded,
				MemberCluster:    multicluster.GetLegacyCentralMemberCluster(0, 0, nil, sc),
				SecretReadClient: sc,
				CertOptions:      shardedOptions,
			},
		},
		{
			name:          "multi-cluster-replica-set",
			resource:      mrs,
			issuedOptions: []Options{MultiReplicaSetManagedCertConfig(*mrs, mrsClusters)},
			// The controller writes the -pem secrets per member cluster, from the per-cluster options.
			pemOptions: []Options{
				MultiReplicaSetConfig(*mrs, 0, "test-cluster-0", 3),
				MultiReplicaSetConfig(*mrs, 1, "test-cluster-1", 3),
			},
			configurator: MongoDBMultiX509CertConfigurator{
				MongoDBMultiCluster: mrs,
				ClusterNum:          0,
				ClusterName:         "test-cluster-0",
				Replicas:            3,
				SecretReadClient:    sc,
				SecretWriteClient:   sc,
			},
			multiCluster: true,
		},
		{
			name:     "multi-cluster-sharded-cluster",
			resource: mcSharded,
			issuedOptions: []Options{
				MultiShardedComponentManagedCertConfig(*mcSharded, mcSharded.MongosRsName(), mcShardedClusters),
				MultiShardedComponentManagedCertConfig(*mcSharded, mcSharded.ConfigRsName(), mcShardedClusters),
				MultiShardedComponentManagedCertConfig(*mcSharded, mcSharded.ShardName(0), mcShardedClusters),
			},
			pemOptions: []Options{
				MongosConfig(*mcSharded, nil, clusterScaler{fakeScaler{current: 1, desired: 1}, 0, "test-cluster-0"}),
				ConfigSrvConfig(*mcSharded, nil, clusterScaler{fakeScaler{current: 3, desired: 3}, 0, "test-cluster-0"}),
				ShardConfig(*mcSharded, 0, nil, clusterScaler{fakeScaler{current: 3, desired: 3}, 0, "test-cluster-0"}),
				MongosConfig(*mcSharded, nil, clusterScaler{fakeScaler{current: 1, desired: 1}, 1, "test-cluster-1"}),
				ConfigSrvConfig(*mcSharded, nil, clusterScaler{fakeScaler{current: 3, desired: 3}, 1, "test-cluster-1"}),
				ShardConfig(*mcSharded, 0, nil, clusterScaler{fakeScaler{current: 3, desired: 3}, 1, "test-cluster-1"}),
			},
			configurator: ShardedSetX509CertConfigurator{
				MongoDB:          mcSharded,
				MemberCluster:    multicluster.MemberCluster{Name: "test-cluster-0", SecretClient: sc},
				SecretReadClient: sc,
			},
			multiCluster: true,
		},
	}
}

// managedCertSecurity is the security section of a deployment that uses operator-managed certificates
// with x509 internal cluster authentication.
func managedCertSecurity() *mdbv1.Security {
	return &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true},
		Authentication: &mdbv1.Authentication{
			Enabled:         true,
			Modes:           []mdbv1.AuthMode{util.X509},
			InternalCluster: util.X509,
			Agents:          mdbv1.AgentAuthentication{Mode: util.X509},
		},
		ManagedCertificate: &mdbv1.ManagedCertificate{},
	}
}

// multiClusterReplicaSet builds the resource of a multi-cluster replica set. There is no builder for
// it, so it is assembled here, with the same cluster list the multi-cluster sharded builder uses.
func multiClusterReplicaSet(name string, security *mdbv1.Security) *mdbmulti.MongoDBMultiCluster {
	mrs := &mdbmulti.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: managedCertTestNamespace,
			UID:       managedCertTestUID,
		},
		Spec: mdbmulti.MongoDBMultiSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version:      "6.0.0",
				ResourceType: mdbv1.ReplicaSet,
				Security:     security,
			},
			ClusterSpecList: mdbv1.ClusterSpecList{
				{ClusterName: "test-cluster-0", Members: 3},
				{ClusterName: "test-cluster-1", Members: 3},
			},
		},
	}
	mrs.InitDefaults()
	return mrs
}

// clusterScaler is the scaler of one member cluster: the per-cluster configs ask the scaler which
// member cluster they are building for.
type clusterScaler struct {
	fakeScaler
	clusterNum  int
	clusterName string
}

func (c clusterScaler) MemberClusterNum() int     { return c.clusterNum }
func (c clusterScaler) MemberClusterName() string { return c.clusterName }

// assertCertSpecCarriesOwnerLabels checks that the Certificate stamps the resource's labels on the
// secret it is issued into. cert-manager writes them there, and they are what the cleanup on deletion
// matches the copies in the member clusters by.
func assertCertSpecCarriesOwnerLabels(t *testing.T, res CertificateOwner, cert certToEnsure) {
	t.Helper()
	require.NotNil(t, cert.spec.SecretTemplate,
		"certificate %s has no secretTemplate, so the secret it is issued into carries no labels", cert.certName)
	assert.Equal(t, res.GetOwnerLabels(), cert.spec.SecretTemplate.Labels,
		"certificate %s has to stamp the resource's owner labels on the secret it is issued into", cert.certName)
}

// assertRemovedByOwner checks that Kubernetes deletes the object together with the resource: the
// object is in the cluster that holds the resource and has the owner reference given.
func assertRemovedByOwner(t *testing.T, obj metav1.Object, ownerRefs []metav1.OwnerReference) {
	t.Helper()
	for _, ref := range ownerRefs {
		assert.Contains(t, obj.GetOwnerReferences(), ref,
			"%s is in the cluster that holds %s, so it has to be owned by it, or it outlives it", obj.GetName(), ref.Name)
	}
}

// assertRemovedByLabels checks that the operator's cleanup on deletion removes the object: it is in a
// member cluster, so it must have no owner reference, and it has to carry the labels the cleanup
// looks for.
func assertRemovedByLabels(t *testing.T, obj metav1.Object, res CertificateOwner) {
	t.Helper()
	assert.Equal(t, res.GetOwnerLabels(), obj.GetLabels(),
		"%s is in a member cluster, so it has to carry the labels the cleanup on deletion looks for", obj.GetName())
	assert.Empty(t, obj.GetOwnerReferences(),
		"%s is in a member cluster, where an owner in another cluster cannot be found, so it must have no owner reference", obj.GetName())
}

// createCertManagerSecret creates the secret cert-manager would have written for one of the
// operator's Certificates: a kubernetes.io/tls secret holding a certificate and its key, carrying the
// labels of the Certificate's secretTemplate and no owner, because cert-manager sets none.
func createCertManagerSecret(t *testing.T, c kubernetesClient.Client, name string, labels map[string]string) {
	t.Helper()
	require.NoError(t, c.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: managedCertTestNamespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeTLS,
		Data: selfSignedCertAndKey(t),
	}))
}

func getConfigMap(t *testing.T, c kubernetesClient.Client, name string) corev1.ConfigMap {
	t.Helper()
	cm, err := c.GetConfigMap(context.Background(), types.NamespacedName{Name: name, Namespace: managedCertTestNamespace})
	require.NoError(t, err)
	return cm
}

func getSecret(t *testing.T, c kubernetesClient.Client, name string) corev1.Secret {
	t.Helper()
	s := corev1.Secret{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: name, Namespace: managedCertTestNamespace}, &s))
	return s
}

// selfSignedCertAndKey returns the tls.crt/tls.key pair of a certificate that is complete enough for
// the writers to accept it, and the same certificate as ca.crt, the way cert-manager writes a
// self-signed CA, which is where the CA bundles are read from. It carries no SANs, which is all the test needs: none of the topologies it
// covers is configured with an external domain or additional certificate domains.
func selfSignedCertAndKey(t *testing.T) map[string][]byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return map[string][]byte{
		corev1.TLSCertKey:              certPEM,
		corev1.TLSPrivateKeyKey:        pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		corev1.ServiceAccountRootCAKey: certPEM,
	}
}
