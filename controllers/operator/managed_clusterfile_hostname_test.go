package operator

import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// TestClusterfileHostnameCheck covers the check the operator runs on the internal cluster
// authentication certificate (the clusterfile) when external domains, additional certificate domains
// or replica set horizons are configured.
//
// The clusterfile is a client certificate: a member presents it when it connects to the other
// members, and they recognise it by its subject, not by a hostname. So the clusterfile the operator
// issues has no hostnames, and the check must not ask for any. A clusterfile the customer brings is
// still checked the way it always was.
func TestClusterfileHostnameCheck(t *testing.T) {
	ctx := context.Background()
	externalDomain := "example.com"

	for _, tc := range []struct {
		name string
		// configure builds the configurator and the certificate options the controller of that
		// topology uses for its clusterfile check.
		configure func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options)
	}{
		{
			name: "replica set with an external domain",
			configure: func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options) {
				rs := mdbv1.NewReplicaSetBuilder().SetSecurity(sec).ExposedExternally(nil, nil, &externalDomain).Build()
				configurator := certs.ReplicaSetX509CertConfigurator{MongoDB: rs, SecretClient: sc}
				return configurator, configurator.GetCertOptions()[0]
			},
		},
		{
			name: "replica set with additional certificate domains",
			configure: func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options) {
				sec.TLSConfig.AdditionalCertificateDomains = []string{"additional.example.org"}
				rs := mdbv1.NewReplicaSetBuilder().SetSecurity(sec).Build()
				configurator := certs.ReplicaSetX509CertConfigurator{MongoDB: rs, SecretClient: sc}
				return configurator, configurator.GetCertOptions()[0]
			},
		},
		{
			name: "replica set with horizons",
			configure: func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options) {
				rs := mdbv1.NewReplicaSetBuilder().SetSecurity(sec).Build()
				rs.Spec.Connectivity = &mdbv1.MongoDBConnectivity{ReplicaSetHorizons: []mdbv1.MongoDBHorizonConfig{
					{"external": "member-0.example.com:27017"},
					{"external": "member-1.example.com:27017"},
					{"external": "member-2.example.com:27017"},
				}}
				configurator := certs.ReplicaSetX509CertConfigurator{MongoDB: rs, SecretClient: sc}
				return configurator, configurator.GetCertOptions()[0]
			},
		},
		{
			name: "sharded cluster with an external domain",
			configure: func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options) {
				sharded := mdbv1.NewDefaultShardedClusterBuilder().SetSecurity(sec).ExposedExternally(nil, nil, &externalDomain).Build()
				opts := certs.ShardConfig(*sharded, 0, &externalDomain, stubScaler{current: 3, target: 3}, nil)
				configurator := certs.ShardedSetX509CertConfigurator{
					MongoDB:          sharded,
					MemberCluster:    multicluster.MemberCluster{SecretClient: sc},
					SecretReadClient: sc,
					CertOptions:      []certs.Options{opts},
				}
				return configurator, opts
			},
		},
		{
			name: "multi-cluster replica set with an external domain in one cluster",
			configure: func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options) {
				mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetSecurity(sec).Build()
				mrs.Spec.ClusterSpecList = mdbv1.ClusterSpecList{{
					ClusterName:                 "cluster-0",
					Members:                     3,
					ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{ExternalDomain: &externalDomain},
				}}
				configurator := certs.MongoDBMultiX509CertConfigurator{
					MongoDBMultiCluster: mrs,
					ClusterNum:          0,
					ClusterName:         "cluster-0",
					Replicas:            3,
					SecretReadClient:    sc,
					SecretWriteClient:   sc,
				}
				return configurator, configurator.GetCertOptions()[0]
			},
		},
		{
			name: "multi-cluster sharded cluster with an external domain",
			configure: func(sec *mdbv1.Security, sc secrets.SecretClient) (certs.X509CertConfigurator, certs.Options) {
				sharded := mdbv1.NewDefaultMultiShardedClusterBuilder().SetSecurity(sec).Build()
				opts := certs.ShardConfig(*sharded, 0, &externalDomain, stubScaler{current: 3, target: 3}, nil)
				configurator := certs.ShardedSetX509CertConfigurator{
					MongoDB:          sharded,
					MemberCluster:    multicluster.MemberCluster{SecretClient: sc},
					SecretReadClient: sc,
					CertOptions:      []certs.Options{opts},
				}
				return configurator, opts
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("operator-managed clusterfile with no hostnames is accepted", func(t *testing.T) {
				r, sc := newClusterfileCheckController(ctx)
				configurator, opts := tc.configure(clusterfileTestSecurity(true), sc)
				putClusterfileWithoutHostnames(ctx, t, sc, opts)

				assert.NoError(t, r.validateInternalClusterCertsAndCheckTLSType(ctx, configurator, opts, zap.S()))
			})

			t.Run("customer clusterfile is still checked for the hostnames", func(t *testing.T) {
				r, sc := newClusterfileCheckController(ctx)
				configurator, opts := tc.configure(clusterfileTestSecurity(false), sc)
				putClusterfileWithoutHostnames(ctx, t, sc, opts)

				err := r.validateInternalClusterCertsAndCheckTLSType(ctx, configurator, opts, zap.S())
				require.Error(t, err)
				assert.Contains(t, err.Error(), "is not contained in the list of DNSNames")
			})
		})
	}
}

// clusterfileTestSecurity is TLS with x509 internal cluster authentication, which is what makes the
// deployment use a clusterfile, with or without operator-managed certificates.
func clusterfileTestSecurity(managed bool) *mdbv1.Security {
	sec := &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true},
		Authentication: &mdbv1.Authentication{
			Enabled:         true,
			Modes:           []mdbv1.AuthMode{util.X509},
			InternalCluster: util.X509,
		},
	}
	if managed {
		sec.ManagedCertificate = &mdbv1.ManagedCertificate{}
	}
	return sec
}

func newClusterfileCheckController(ctx context.Context) (*ReconcileCommonController, secrets.SecretClient) {
	kubeClient, _ := mock.NewDefaultFakeClient()
	return NewReconcileCommonController(ctx, kubeClient), secrets.SecretClient{KubeClient: kubeClient}
}

// putClusterfileWithoutHostnames writes the clusterfile secret the options name, holding a valid
// certificate with no DNS names, the way the operator issues it.
func putClusterfileWithoutHostnames(ctx context.Context, t *testing.T, sc secrets.SecretClient, opts certs.Options) {
	t.Helper()
	cert, key := createMockCertAndKeyBytes(func(c *x509.Certificate) { c.DNSNames = nil })
	s := secret.Builder().
		SetName(opts.InternalClusterSecretName).
		SetNamespace(opts.Namespace).
		SetDataType(corev1.SecretTypeTLS).
		SetByteData(map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}).
		Build()
	require.NoError(t, sc.KubeClient.CreateSecret(ctx, s))
}
