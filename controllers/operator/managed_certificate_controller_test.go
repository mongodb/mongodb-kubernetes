package operator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	operatorConstruct "github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// issueLeafSignedByCA returns a leaf certificate signed by the given CA certificate and key. The leaf
// carries the same organization as the mock certificates, so it also passes the membership subject check.
func issueLeafSignedByCA(t *testing.T, caCertPEM, caKeyPEM []byte, commonName string) []byte {
	t.Helper()
	block, _ := pem.Decode(caCertPEM)
	require.NotNil(t, block)
	caCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	keyBlock, _ := pem.Decode(caKeyPEM)
	require.NotNil(t, keyBlock)
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	require.NoError(t, err)
	caKey, ok := parsedKey.(*ecdsa.PrivateKey)
	require.True(t, ok, "the mock CA key must be an ECDSA key")

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{Organization: []string{"MongoDB"}, CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// issueManagedCertificates plays cert-manager: it marks every Certificate in the namespace as Ready and
// creates the secret it points at, with a fresh keypair. Existing secrets are left as they are.
func issueManagedCertificates(ctx context.Context, t *testing.T, c kubernetesClient.Client, namespace string) {
	certList := &certmanagerv1.CertificateList{}
	require.NoError(t, c.List(ctx, certList))
	for i := range certList.Items {
		cert := &certList.Items[i]
		cert.Status.Conditions = []certmanagerv1.CertificateCondition{{
			Type:               certmanagerv1.CertificateConditionReady,
			Status:             cmmeta.ConditionTrue,
			ObservedGeneration: cert.Generation,
		}}
		require.NoError(t, c.Status().Update(ctx, cert))

		crt, key := createMockCertAndKeyBytes(func(cert *x509.Certificate) {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
		})
		ca, _ := createMockCertAndKeyBytes()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cert.Spec.SecretName, Namespace: namespace},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key, corev1.ServiceAccountRootCAKey: ca},
		}
		if err := c.Create(ctx, secret); err != nil {
			require.True(t, apiErrors.IsAlreadyExists(err), "unexpected error: %v", err)
		}
	}
}

func TestDatabaseCAFilePaths(t *testing.T) {
	ctx := context.Background()
	const name = "mdb"
	kubeClient, _ := mock.NewDefaultFakeClient()

	serverCM := configmap.Builder().SetName(certs.ManagedServerCABundleConfigMapName(name)).SetNamespace(mock.TestNamespace).
		SetData(map[string]string{util.LatestHashSecretKey: "SERVERHASH", "SERVERHASH": "bundle"}).Build()
	clientCM := configmap.Builder().SetName(certs.ManagedClientCABundleConfigMapName(name)).SetNamespace(mock.TestNamespace).
		SetData(map[string]string{util.LatestHashSecretKey: "CLIENTHASH", "CLIENTHASH": "bundle"}).Build()
	require.NoError(t, kubeClient.CreateConfigMap(ctx, serverCM))
	require.NoError(t, kubeClient.CreateConfigMap(ctx, clientCM))

	x509 := &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}}

	t.Run("non-managed keeps the fixed ca-pem path and no cluster CA", func(t *testing.T) {
		ca, clusterCA, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, path.Join(util.TLSCaMountPath, "ca-pem"), ca)
		assert.Empty(t, clusterCA)
	})

	t.Run("non-managed honours the user caFilePath", func(t *testing.T) {
		ca, _, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true, CAFilePath: "/custom/ca"}}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, "/custom/ca", ca)
	})

	t.Run("managed uses the server bundle hash, no cluster CA without client certs", func(t *testing.T) {
		ca, clusterCA, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, util.TLSCaMountPath+"/SERVERHASH", ca)
		assert.Empty(t, clusterCA)
	})

	t.Run("managed with client certs also uses the client bundle hash", func(t *testing.T) {
		ca, clusterCA, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}, Authentication: x509}, name, mock.TestNamespace)
		require.NoError(t, err)
		assert.Equal(t, util.TLSCaMountPath+"/SERVERHASH", ca)
		assert.Equal(t, util.TLSClusterCaMountPath+"/CLIENTHASH", clusterCA)
	})

	t.Run("managed fails when the bundles are not published", func(t *testing.T) {
		_, _, err := databaseCAFilePaths(ctx, kubeClient, &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}, "other", mock.TestNamespace)
		assert.Error(t, err)
	})
}

func TestManagedCARolloutOrdering(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		publishFirst, caChanged bool
		stopAt                  string
		want                    []string
	}{
		{"normal reconcile", false, false, "", []string{"pods", "converge"}},
		{"scale-down order", true, false, "", []string{"converge", "pods"}},
		{"rotate trust", true, true, "", []string{"publish", "pods", "converge"}},
		{"publish failed", true, true, "publish", []string{"publish"}},
		{"roll incomplete", true, true, "pods", []string{"publish", "pods"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var steps []string
			run := func(step string) workflow.Status {
				steps = append(steps, step)
				if tt.stopAt == step {
					return workflow.Pending("waiting for %s", step)
				}
				return workflow.OK()
			}
			status := reconcileDatabaseRollout(tt.publishFirst, tt.caChanged, func(wait bool) workflow.Status {
				if wait {
					return run("converge")
				}
				return run("publish")
			}, func() workflow.Status { return run("pods") })
			assert.Equal(t, tt.want, steps)
			assert.Equal(t, tt.stopAt == "", status.IsOK())
		})
	}
}

func TestManagedCABundlePodsRolled(t *testing.T) {
	ctx := t.Context()
	const (
		namespace = mock.TestNamespace
		stsName   = "rs"
	)
	kubeClient, _ := mock.NewDefaultFakeClient()
	rolled, err := managedCABundlePodsRolled(ctx, kubeClient, namespace, stsName, "new-server", "new-client")
	require.NoError(t, err)
	assert.False(t, rolled, "a missing StatefulSet does not confirm rollout")

	replicas := int32(2)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: stsName, Namespace: namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				operatorConstruct.ManagedServerCABundleHashAnnotation: "new-server",
				operatorConstruct.ManagedClientCABundleHashAnnotation: "new-client",
			}}},
		},
	}
	require.NoError(t, kubeClient.Create(ctx, sts))

	pod := func(ordinal int, serverHash, clientHash string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%d", stsName, ordinal),
			Namespace:   namespace,
			Annotations: map[string]string{operatorConstruct.ManagedServerCABundleHashAnnotation: serverHash, operatorConstruct.ManagedClientCABundleHashAnnotation: clientHash},
		}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	}

	require.NoError(t, kubeClient.Create(ctx, pod(0, "new-server", "new-client")))
	rolled, err = managedCABundlePodsRolled(ctx, kubeClient, namespace, stsName, "new-server", "new-client")
	require.NoError(t, err)
	assert.False(t, rolled, "a missing pod must keep the re-issue deferred")

	require.NoError(t, kubeClient.Create(ctx, pod(1, "old-server", "old-client")))
	rolled, err = managedCABundlePodsRolled(ctx, kubeClient, namespace, stsName, "new-server", "new-client")
	require.NoError(t, err)
	assert.False(t, rolled, "a pod still on the previous bundle, as on OnDelete, must keep the re-issue deferred")

	require.NoError(t, kubeClient.Update(ctx, pod(1, "new-server", "new-client")))
	rolled, err = managedCABundlePodsRolled(ctx, kubeClient, namespace, stsName, "new-server", "new-client")
	require.NoError(t, err)
	assert.True(t, rolled)
}

func TestPublishAutomationConfigFirstOnManagedCABundleChange(t *testing.T) {
	ctx := t.Context()
	rs := DefaultReplicaSetBuilder().Build()
	rs.Name = "rs"
	rs.Namespace = mock.TestNamespace
	rs.Spec.Security = &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}
	kubeClient, _ := mock.NewDefaultFakeClient()

	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: rs.Name, Namespace: rs.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{operatorConstruct.ManagedServerCABundleHashAnnotation: "old-server"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: util.DatabaseContainerName}}},
			},
		},
	}
	require.NoError(t, kubeClient.Create(ctx, sts))

	opts := operatorConstruct.DatabaseStatefulSetOptions{
		Name:                      rs.Name,
		Replicas:                  1,
		PodVars:                   &env.PodEnvVars{},
		ManagedServerCABundleHash: "new-server",
	}
	config := func(mdb mdbv1.MongoDB) operatorConstruct.DatabaseStatefulSetOptions { return opts }
	assert.True(t, publishAutomationConfigFirst(ctx, kubeClient, *rs, nil, config, architectures.NonStatic, zap.S()),
		"a managed CA bundle change must publish the automation config before replacing the pods")

	opts.ManagedServerCABundleHash = "old-server"
	assert.False(t, publishAutomationConfigFirst(ctx, kubeClient, *rs, nil, config, architectures.NonStatic, zap.S()),
		"an unchanged managed CA bundle must keep the normal order")
}

func TestShardedAnyStatefulSetNeedsToPublishStateUsesMemberClusterSTS(t *testing.T) {
	ctx := t.Context()
	sc := test.DefaultClusterBuilder().Build()
	sc.Spec.Security = &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}}

	member, _ := mock.NewDefaultFakeClient(sc)
	const stsName = "sc-0"
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: stsName, Namespace: sc.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{operatorConstruct.ManagedServerCABundleHashAnnotation: "old-server"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: util.DatabaseContainerName}}},
			},
		},
	}
	require.NoError(t, member.Create(ctx, sts))

	opts := operatorConstruct.DatabaseStatefulSetOptions{
		Name:                      stsName,
		Replicas:                  1,
		PodVars:                   &env.PodEnvVars{},
		ManagedServerCABundleHash: "new-server",
	}
	configs := []shardedStatefulSetConfig{{
		client:  member,
		options: func(mdb mdbv1.MongoDB) operatorConstruct.DatabaseStatefulSetOptions { return opts },
	}}
	order, err := anyStatefulSetNeedsToPublishStateToOM(ctx, *sc, nil, configs, architectures.NonStatic, zap.S())
	require.NoError(t, err)
	assert.True(t, order.managedCABundleChange, "the member-cluster StatefulSet must be checked, not the central cluster")
	assert.True(t, order.publishFirst)

	// The central client has no such StatefulSet. Checking only it would miss the change.
	opts.ManagedServerCABundleHash = "old-server"
	order, err = anyStatefulSetNeedsToPublishStateToOM(ctx, *sc, nil, configs, architectures.NonStatic, zap.S())
	require.NoError(t, err)
	assert.False(t, order.managedCABundleChange)
	assert.False(t, order.publishFirst)
}

func TestShardedManagedCABundleRolloutGateIncludesEveryDeclaredCluster(t *testing.T) {
	ctx := t.Context()
	sc := test.DefaultClusterBuilder().SetName("sc").Build()
	sc.Spec.Security = &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}
	kubeClient, _ := mock.NewDefaultFakeClient()
	require.NoError(t, kubeClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: certs.ManagedServerCABundleConfigMapName(sc.Name), Namespace: sc.Namespace},
		Data:       map[string]string{util.LatestHashSecretKey: "new-server"},
	}))

	member := multicluster.GetLegacyCentralMemberCluster(1, 0, kubeClient, secrets.SecretClient{KubeClient: kubeClient})
	helper := &ShardedClusterReconcileHelper{
		commonController:           &ReconcileCommonController{client: kubeClient},
		sc:                         sc,
		desiredShardsConfiguration: map[int]*mdbv1.ShardedClusterComponentSpec{0: {}},
		shardsMemberClustersMap:    map[int][]multicluster.MemberCluster{0: {member}},
		configSrvMemberClusters:    []multicluster.MemberCluster{member},
		mongosMemberClusters:       []multicluster.MemberCluster{member},
	}

	replicas := int32(1)
	for _, name := range []string{sc.ShardName(0), sc.ConfigRsName(), sc.MongosRsName()} {
		require.NoError(t, kubeClient.Create(ctx, &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sc.Namespace},
			Spec: appsv1.StatefulSetSpec{
				Replicas:       &replicas,
				UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					operatorConstruct.ManagedServerCABundleHashAnnotation: "new-server",
				}}},
			},
		}))
		require.NoError(t, kubeClient.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-0", name),
			Namespace:   sc.Namespace,
			Annotations: map[string]string{operatorConstruct.ManagedServerCABundleHashAnnotation: "new-server"},
		}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}))
	}

	rolled, err := helper.managedCABundleRolloutComplete(ctx, sc, zap.S())
	require.NoError(t, err)
	assert.True(t, rolled)

	// A declared member cluster the operator cannot reach must keep the re-issue deferred.
	helper.mongosMemberClusters = []multicluster.MemberCluster{{Name: "unreachable", Replicas: 1, Index: 1, Legacy: false, Client: nil}}
	rolled, err = helper.managedCABundleRolloutComplete(ctx, sc, zap.S())
	require.NoError(t, err)
	assert.False(t, rolled, "a still-declared unreachable member cluster must not be skipped")
}

func TestManagedDatabaseCABundleHashes(t *testing.T) {
	ctx := context.Background()
	const name = "mdb"
	kubeClient, _ := mock.NewDefaultFakeClient()

	require.NoError(t, kubeClient.CreateConfigMap(ctx, configmap.Builder().
		SetName(certs.ManagedServerCABundleConfigMapName(name)).SetNamespace(mock.TestNamespace).
		SetData(map[string]string{util.LatestHashSecretKey: "SERVERHASH", "SERVERHASH": "bundle"}).Build()))
	require.NoError(t, kubeClient.CreateConfigMap(ctx, configmap.Builder().
		SetName(certs.ManagedClientCABundleConfigMapName(name)).SetNamespace(mock.TestNamespace).
		SetData(map[string]string{util.LatestHashSecretKey: "CLIENTHASH", "CLIENTHASH": "bundle"}).Build()))

	x509 := &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}}
	tests := []struct {
		name       string
		security   *mdbv1.Security
		resource   string
		wantServer string
		wantClient string
		wantErr    bool
	}{
		{
			name:     "unmanaged TLS has no managed bundle",
			security: &mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}},
		},
		{
			name:       "managed without client certificates returns only the server bundle",
			security:   &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}},
			wantServer: "SERVERHASH",
		},
		{
			name:       "managed with client certificates returns both bundles",
			security:   &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}, Authentication: x509},
			wantServer: "SERVERHASH",
			wantClient: "CLIENTHASH",
		},
		{
			name:     "managed fails when the bundles are not published",
			security: &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}},
			resource: "other",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource := tt.resource
			if resource == "" {
				resource = name
			}
			server, client, err := managedDatabaseCABundleHashes(ctx, kubeClient, tt.security, resource, mock.TestNamespace)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantServer, server)
			assert.Equal(t, tt.wantClient, client)
		})
	}
}

// TestReplicaSetManagedCertificate_CAFilePathFollowsBundleHash checks that the automation config
// CA paths are the hash-named bundle files, that a CA rotation retains the old CA next to the new
// one, and that the prune annotation then drops it.
func TestReplicaSetManagedCertificate_CAFilePathFollowsBundleHash(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}, InternalCluster: util.X509},
	}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, omConnectionFactory.GetConnectionFunc, nil, certs.CertManagerReadiness{Ready: true})
	addKubernetesTlsResources(ctx, kubeClient, rs)

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, rs.Namespace)

	// replace the CA cert-manager issued with one the test controls, so it can also sign a leaf with it
	serverCASecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA, oldCAKey := createMockCertAndKeyBytes()
	serverCASecret.Data[corev1.ServiceAccountRootCAKey] = oldCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, serverCASecret))

	reconcileAndReadTLS := func() map[string]any {
		_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
		return omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	}
	serverBundle := func() string {
		cm, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(rs.Namespace, certs.ManagedServerCABundleConfigMapName(rs.Name)))
		require.NoError(t, err)
		return cm.Data[cm.Data[util.LatestHashSecretKey]]
	}
	stsPodAnnotations := func() map[string]string {
		sts := &appsv1.StatefulSet{}
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(rs.Namespace, rs.Name), sts))
		return sts.Spec.Template.Annotations
	}

	tlsConfig := reconcileAndReadTLS()
	hashesServer, hashesClient, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	require.NotEmpty(t, hashesServer)
	require.NotEmpty(t, hashesClient)
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])
	assert.Equal(t, util.TLSClusterCaMountPath+"/"+hashesClient, tlsConfig["clusterCAFilePath"])
	podAnnotations := stsPodAnnotations()
	assert.Equal(t, hashesServer, podAnnotations[operatorConstruct.ManagedServerCABundleHashAnnotation])
	assert.Equal(t, hashesClient, podAnnotations[operatorConstruct.ManagedClientCABundleHashAnnotation])

	// the issuer's CA is rotated: the old CA is retained next to the new one, and the CA path moves
	newCA, newCAKey := createMockCertAndKeyBytes()
	serverCASecret, err = kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-server-ca"))
	require.NoError(t, err)
	serverCASecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, serverCASecret))

	tlsConfig = reconcileAndReadTLS()
	newHashesServer, newHashesClient, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	assert.NotEqual(t, hashesServer, newHashesServer)
	assert.Equal(t, hashesClient, newHashesClient)
	assert.Equal(t, util.TLSCaMountPath+"/"+newHashesServer, tlsConfig["CAFilePath"])
	assert.Equal(t, util.TLSClusterCaMountPath+"/"+hashesClient, tlsConfig["clusterCAFilePath"])
	podAnnotations = stsPodAnnotations()
	assert.Equal(t, newHashesServer, podAnnotations[operatorConstruct.ManagedServerCABundleHashAnnotation],
		"a widened server bundle must roll the members")
	assert.Equal(t, hashesClient, podAnnotations[operatorConstruct.ManagedClientCABundleHashAnnotation],
		"a server-only rotation must not roll the members for the client bundle")
	rotatedBundle := serverBundle()
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is retained after the rotation")
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(newCA)), "the new CA is published")
	assert.Equal(t, enterprisepem.HashBytes([]byte(rotatedBundle)), newHashesServer)

	// the condition tells the user that a previous CA is retained and how to prune it
	certsCondition := CertificatesReadyCondition(t, rs)
	assert.Equal(t, metav1.ConditionTrue, certsCondition.Status)
	assert.Contains(t, certsCondition.Message, "Previous server CA retained in the trust bundle")
	assert.Contains(t, certsCondition.Message, util.PrunePreviousCAAnnotation+"=server (or true)")

	// the member certificate is still signed by the old CA: the prune is refused and the annotation stays
	memberSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-managed-cert"))
	require.NoError(t, err)
	memberSecret.Data[corev1.TLSCertKey] = issueLeafSignedByCA(t, oldCA, oldCAKey, "member")
	require.NoError(t, kubeClient.UpdateSecret(ctx, memberSecret))
	if rs.Annotations == nil {
		rs.Annotations = map[string]string{}
	}
	rs.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, rs))

	reconcileAndReadTLS()
	assert.Contains(t, serverBundle(), strings.TrimSpace(string(oldCA)), "the previous CA is kept while a leaf is signed by it")
	assert.Equal(t, "true", rs.GetAnnotations()[util.PrunePreviousCAAnnotation], "the annotation stays while the prune is refused")
	certsCondition = CertificatesReadyCondition(t, rs)
	assert.Contains(t, certsCondition.Message, "The previous server CA was not pruned: issued certificates are still signed by it.")

	// reissue the member certificate from the new CA, then the operator prunes the previous CA
	memberSecret, err = kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-managed-cert"))
	require.NoError(t, err)
	memberSecret.Data[corev1.TLSCertKey] = issueLeafSignedByCA(t, newCA, newCAKey, "member")
	require.NoError(t, kubeClient.UpdateSecret(ctx, memberSecret))

	tlsConfig = reconcileAndReadTLS()
	prunedHashesServer, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, rs.Name, rs.Namespace, true)
	require.NoError(t, err)
	assert.NotEqual(t, newHashesServer, prunedHashesServer)
	assert.Equal(t, util.TLSCaMountPath+"/"+prunedHashesServer, tlsConfig["CAFilePath"])
	assert.Equal(t, prunedHashesServer, stsPodAnnotations()[operatorConstruct.ManagedServerCABundleHashAnnotation],
		"a pruned bundle must roll the members")
	prunedBundle := serverBundle()
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
	assert.Equal(t, enterprisepem.HashBytes([]byte(prunedBundle)), prunedHashesServer)

	// the operator removes the annotation once the prune has been applied
	assert.NotContains(t, rs.GetAnnotations(), util.PrunePreviousCAAnnotation)
	certsCondition = CertificatesReadyCondition(t, rs)
	assert.NotContains(t, certsCondition.Message, "Previous server CA retained")
}

func TestReplicaSetManagedCertificate_ClientPrunePublishesPEMBeforeAgentWait(t *testing.T) {
	ctx := t.Context()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}, InternalCluster: util.X509},
	}).Build()
	c, factory := mock.NewDefaultFakeClient(rs)
	r := newReplicaSetReconciler(ctx, c, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, factory.GetConnectionFunc, nil, certs.CertManagerReadiness{Ready: true})
	addKubernetesTlsResources(ctx, c, rs)
	reconcile := func() {
		_, err := r.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		require.NoError(t, c.Get(ctx, kube.ObjectKeyFromApiObject(rs), rs))
	}
	reconcile()
	issueManagedCertificates(ctx, t, c, rs.Namespace)
	clusterfile := rs.GetSecurity().InternalClusterAuthSecretName(rs.Name)
	agent := rs.GetSecurity().AgentClientCertificateSecretName(rs.Name)
	setRoot := func(root []byte) {
		s, err := c.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-client-ca"))
		require.NoError(t, err)
		s.Data[corev1.ServiceAccountRootCAKey] = root
		require.NoError(t, c.UpdateSecret(ctx, s))
	}
	issueLeaves := func(root, key []byte) {
		for _, name := range []string{agent, clusterfile} {
			s, err := c.GetSecret(ctx, kube.ObjectKey(rs.Namespace, name))
			require.NoError(t, err)
			s.Data[corev1.TLSCertKey] = issueLeafSignedByCA(t, root, key, name)
			require.NoError(t, c.UpdateSecret(ctx, s))
		}
	}
	oldRoot, oldKey := createMockCertAndKeyBytes()
	setRoot(oldRoot)
	issueLeaves(oldRoot, oldKey)
	reconcile()
	newRoot, newKey := createMockCertAndKeyBytes()
	setRoot(newRoot)
	reconcile()
	rs.Annotations[util.PrunePreviousCAAnnotation] = "client"
	require.NoError(t, c.Update(ctx, rs))
	reconcile()
	require.Equal(t, "client", rs.Annotations[util.PrunePreviousCAAnnotation])
	issueLeaves(newRoot, newKey)

	conn := factory.GetConnection().(*om.MockedOmConnection)
	ready, err := conn.ReadAutomationStatus(ctx)
	require.NoError(t, err)
	oldPEM, err := c.GetSecret(ctx, kube.ObjectKey(rs.Namespace, clusterfile+certs.OperatorGeneratedCertSuffix))
	require.NoError(t, err)
	oldHash := string(oldPEM.Data[util.LatestHashSecretKey])
	sawNewPath := false
	conn.ReadAutomationStatusFunc = func() (*om.AutomationStatus, error) {
		d := conn.GetDeployment()
		file := d.GetInternalClusterFilePath(d.GetProcessNames(om.ReplicaSet{}, rs.GetReplicaSetName()))
		if hash := path.Base(file); file != "" && hash != oldHash {
			sawNewPath = true
			pem, err := c.GetSecret(ctx, kube.ObjectKey(rs.Namespace, clusterfile+certs.OperatorGeneratedCertSuffix))
			require.NoError(t, err)
			assert.NotEmpty(t, pem.Data[hash], "agents cannot reach goal when clusterFile is absent from the mounted Secret")
			assert.NotEmpty(t, pem.Data[oldHash], "agents still on the previous config need the old file")
		}
		return ready, nil
	}
	reconcile()
	assert.True(t, sawNewPath)
	assert.NotContains(t, rs.Annotations, util.PrunePreviousCAAnnotation)
}

// TestShardedClusterManagedCertificate_CAFilePathIsBundleHash checks that the published automation
// config of a sharded cluster points at the hash-named server CA bundle.
func TestShardedClusterManagedCertificate_CAFilePathIsBundleHash(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().SetSecurity(mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	reconciler := newShardedClusterReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, nil, omConnectionFactory.GetConnectionFunc, testBackupEnableDelay, nil, certs.CertManagerReadiness{Ready: true})

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err := reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, sc.Namespace)

	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)

	hashesServer, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, sc.Name, sc.Namespace, false)
	require.NoError(t, err)
	require.NotEmpty(t, hashesServer)
	componentNames := []string{sc.Name + "-mongos", sc.Name + "-config", sc.Name + "-0", sc.Name + "-1"}
	assertComponentAnnotations := func(serverHash string) {
		for _, name := range componentNames {
			sts := &appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(sc.Namespace, name), sts))
			assert.Equal(t, serverHash, sts.Spec.Template.Annotations[operatorConstruct.ManagedServerCABundleHashAnnotation],
				"%s must roll on the shared server CA bundle", name)
			assert.NotContains(t, sts.Spec.Template.Annotations, operatorConstruct.ManagedClientCABundleHashAnnotation,
				"%s must not roll on a client bundle it does not mount", name)
		}
	}
	assertComponentAnnotations(hashesServer)
	tlsConfig := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])
	assert.NotContains(t, tlsConfig, "clusterCAFilePath")

	serverCM, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
	require.NoError(t, err)
	assert.Equal(t, hashesServer, serverCM.Data[util.LatestHashSecretKey])
	assert.NotEmpty(t, serverCM.Data[hashesServer])

	// rotate the server CA: the previous one is retained in the bundle
	caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(sc.Namespace, sc.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA := caSecret.Data[corev1.ServiceAccountRootCAKey]
	newCA, _ := createMockCertAndKeyBytes()
	caSecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))

	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	serverCM, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
	require.NoError(t, err)
	rotatedBundle := serverCM.Data[serverCM.Data[util.LatestHashSecretKey]]
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)))
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(newCA)))
	rotatedHashes, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, sc.Name, sc.Namespace, false)
	require.NoError(t, err)
	assert.NotEqual(t, hashesServer, rotatedHashes)
	assertComponentAnnotations(rotatedHashes)

	// asking to prune removes the previous CA and the annotation
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(sc), sc))
	if sc.Annotations == nil {
		sc.Annotations = map[string]string{}
	}
	sc.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, sc))

	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(sc), sc))
	assert.NotContains(t, sc.GetAnnotations(), util.PrunePreviousCAAnnotation)

	serverCM, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(sc.Namespace, certs.ManagedServerCABundleConfigMapName(sc.Name)))
	require.NoError(t, err)
	prunedBundle := serverCM.Data[serverCM.Data[util.LatestHashSecretKey]]
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
	prunedHashes, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, sc.Name, sc.Namespace, false)
	require.NoError(t, err)
	assert.NotEqual(t, rotatedHashes, prunedHashes)
	assertComponentAnnotations(prunedHashes)
}

// TestMultiReplicaSetManagedCertificate_CAFilePathIsBundleHash checks that the published automation
// config of a multi-cluster replica set points at the hash-named server CA bundle, and that this bundle
// was replicated to the member clusters.
func TestMultiReplicaSetManagedCertificate_CAFilePathIsBundleHash(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetSecurity(&mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}).
		Build()

	reconciler, kubeClient, memberClients, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs, architectures.NonStatic)

	// the first reconcile creates the Certificates, then cert-manager issues them
	_, err := reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, mrs.Namespace)

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)

	hashesServer, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, mrs.Name, mrs.Namespace, false)
	require.NoError(t, err)
	require.NotEmpty(t, hashesServer)
	tlsConfig := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	assert.Equal(t, util.TLSCaMountPath+"/"+hashesServer, tlsConfig["CAFilePath"])

	central, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	assert.NotEmpty(t, central.Data[hashesServer])
	assertMemberStatefulSetAnnotations := func(serverHash string) {
		for _, clusterName := range clusters {
			memberClient := memberClients[clusterName]
			name := mrs.MultiStatefulsetName(mrs.ClusterNum(clusterName))
			sts, err := kubernetesClient.NewClient(memberClient).GetStatefulSet(ctx, kube.ObjectKey(mrs.Namespace, name))
			require.NoError(t, err, clusterName)
			assert.Equal(t, serverHash, sts.Spec.Template.Annotations[operatorConstruct.ManagedServerCABundleHashAnnotation],
				"%s must roll the members in %s on the replicated bundle", name, clusterName)
		}
	}
	assertMemberStatefulSetAnnotations(hashesServer)
	for clusterName, memberClient := range memberClients {
		member, err := kubernetesClient.NewClient(memberClient).GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
		require.NoError(t, err, clusterName)
		assert.Equal(t, central.Data, member.Data, clusterName)
		assert.Contains(t, member.Data, hashesServer, clusterName)
	}

	// rotate the central server CA: the previous one is retained and replicated to every member cluster
	caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name+"-server-ca"))
	require.NoError(t, err)
	oldCA := caSecret.Data[corev1.ServiceAccountRootCAKey]
	newCA, _ := createMockCertAndKeyBytes()
	caSecret.Data[corev1.ServiceAccountRootCAKey] = newCA
	require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	central, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	rotatedBundle := central.Data[central.Data[util.LatestHashSecretKey]]
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)))
	rotatedHashes, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, mrs.Name, mrs.Namespace, false)
	require.NoError(t, err)
	assert.NotEqual(t, hashesServer, rotatedHashes)
	assertMemberStatefulSetAnnotations(rotatedHashes)

	// asking to prune removes the previous CA everywhere and the annotation
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(mrs), mrs))
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}
	mrs.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, mrs))

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(mrs), mrs))
	assert.NotContains(t, mrs.GetAnnotations(), util.PrunePreviousCAAnnotation)

	central, err = kubeClient.GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
	require.NoError(t, err)
	prunedBundle := central.Data[central.Data[util.LatestHashSecretKey]]
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous CA is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
	prunedHashes, _, err := certs.ReadManagedCABundleHashes(ctx, kubeClient, mrs.Name, mrs.Namespace, false)
	require.NoError(t, err)
	assert.NotEqual(t, rotatedHashes, prunedHashes)
	assertMemberStatefulSetAnnotations(prunedHashes)
	for clusterName, memberClient := range memberClients {
		member, err := kubernetesClient.NewClient(memberClient).GetConfigMap(ctx, kube.ObjectKey(mrs.Namespace, certs.ManagedServerCABundleConfigMapName(mrs.Name)))
		require.NoError(t, err, clusterName)
		assert.Equal(t, central.Data, member.Data, clusterName)
	}
}

// TestReplicaSetManagedCertificate_ReissuesLeavesOnCARotation covers the second half of an in-place
// self-signed CA rotation: once the widened bundle has reached the members, the operator asks
// cert-manager to reissue the leaves that are still signed by the previous root, and stops asking
// once cert-manager has written a new-root certificate into the secret.
func TestReplicaSetManagedCertificate_ReissuesLeavesOnCARotation(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
		Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}, InternalCluster: util.X509},
	}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, omConnectionFactory.GetConnectionFunc, nil, certs.CertManagerReadiness{Ready: true})
	addKubernetesTlsResources(ctx, kubeClient, rs)

	memberSecretName := rs.Name + "-managed-cert"
	memberCertKey := kube.ObjectKey(rs.Namespace, memberSecretName)

	// The first reconcile creates the Certificates, then cert-manager issues them.
	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, rs.Namespace)

	oldCA, oldCAKey := createMockCertAndKeyBytes()
	newCA, newCAKey := createMockCertAndKeyBytes()

	setServerRoot := func(ca []byte) {
		caSecret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(rs.Namespace, rs.Name+"-server-ca"))
		require.NoError(t, err)
		caSecret.Data[corev1.ServiceAccountRootCAKey] = ca
		caSecret.Data[corev1.TLSCertKey] = ca
		require.NoError(t, kubeClient.UpdateSecret(ctx, caSecret))
	}
	setMemberLeaf := func(leaf []byte) {
		memberSecret, err := kubeClient.GetSecret(ctx, memberCertKey)
		require.NoError(t, err)
		memberSecret.Data[corev1.TLSCertKey] = leaf
		require.NoError(t, kubeClient.UpdateSecret(ctx, memberSecret))
	}

	// Start from a controlled root with the member leaf signed by it, so the first rotation is
	// the only reason a reissue is requested.
	setServerRoot(oldCA)
	setMemberLeaf(issueLeafSignedByCA(t, oldCA, oldCAKey, "member"))

	reconcileAndReadTLS := func() map[string]any {
		_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
		require.NoError(t, err)
		require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
		return omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetTLS()
	}
	serverBundle := func() string {
		cm, err := kubeClient.GetConfigMap(ctx, kube.ObjectKey(rs.Namespace, certs.ManagedServerCABundleConfigMapName(rs.Name)))
		require.NoError(t, err)
		return cm.Data[cm.Data[util.LatestHashSecretKey]]
	}
	processTLS := func() map[string]any {
		processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
		require.NotEmpty(t, processes)
		return processes[0].TLSConfig()
	}
	processCertificateKeyFile := func() any {
		return processTLS()["certificateKeyFile"]
	}
	memberCertificate := func() *certmanagerv1.Certificate {
		cert := &certmanagerv1.Certificate{}
		require.NoError(t, kubeClient.Get(ctx, memberCertKey, cert))
		return cert
	}
	issuingCondition := func(cert *certmanagerv1.Certificate) *certmanagerv1.CertificateCondition {
		for i := range cert.Status.Conditions {
			if cert.Status.Conditions[i].Type == certmanagerv1.CertificateConditionIssuing {
				return &cert.Status.Conditions[i]
			}
		}
		return nil
	}
	clearIssuing := func() {
		cert := memberCertificate()
		var conditions []certmanagerv1.CertificateCondition
		for _, condition := range cert.Status.Conditions {
			if condition.Type != certmanagerv1.CertificateConditionIssuing {
				conditions = append(conditions, condition)
			}
		}
		cert.Status.Conditions = conditions
		require.NoError(t, kubeClient.Status().Update(ctx, cert))
	}

	beforeRotation := reconcileAndReadTLS()
	assert.Nil(t, issuingCondition(memberCertificate()), "no rotation yet, no reissue")

	// cert-manager renews the root with a new key: the bundle widens, and the operator asks for the
	// member leaf to be reissued because it is still signed by the old root.
	setServerRoot(newCA)
	afterRotation := reconcileAndReadTLS()
	rotatedCertificateKeyFile := processCertificateKeyFile()

	rotatedBundle := serverBundle()
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(oldCA)))
	assert.Contains(t, rotatedBundle, strings.TrimSpace(string(newCA)))
	assert.NotEqual(t, beforeRotation["CAFilePath"], afterRotation["CAFilePath"])

	rotatedCert := memberCertificate()
	issuing := issuingCondition(rotatedCert)
	require.NotNil(t, issuing, "the member certificate must ask cert-manager for a reissue")
	assert.Equal(t, cmmeta.ConditionTrue, issuing.Status)
	assert.Equal(t, "ManagedCARotated", issuing.Reason)
	assert.Equal(t, rotatedCert.Generation, issuing.ObservedGeneration)

	// cert-manager reissues into the same secret and clears Issuing. The operator must not set it
	// again, and the automation config now points at the reissued member certificate.
	setMemberLeaf(issueLeafSignedByCA(t, newCA, newCAKey, "member"))
	clearIssuing()

	reconcileAndReadTLS()
	assert.Nil(t, issuingCondition(memberCertificate()), "the leaf already uses the new root, no further reissue")
	reissuedCertificateKeyFile := processCertificateKeyFile()
	require.NotNil(t, reissuedCertificateKeyFile)
	assert.NotEqual(t, rotatedCertificateKeyFile, reissuedCertificateKeyFile, "the reissued member certificate rolls the members")

	// The previous root can now be pruned.
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
	if rs.Annotations == nil {
		rs.Annotations = map[string]string{}
	}
	rs.Annotations[util.PrunePreviousCAAnnotation] = "true"
	require.NoError(t, kubeClient.Update(ctx, rs))

	reconcileAndReadTLS()
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
	prunedBundle := serverBundle()
	assert.NotContains(t, prunedBundle, strings.TrimSpace(string(oldCA)), "the previous root is pruned")
	assert.Contains(t, prunedBundle, strings.TrimSpace(string(newCA)))
	assert.NotContains(t, rs.GetAnnotations(), util.PrunePreviousCAAnnotation)
}

// setCertificateRenewal plays cert-manager: it records when a Certificate expires and when it will
// renew it, so the operator can report the latter on the resource status.
func setCertificateRenewal(t *testing.T, ctx context.Context, c kubernetesClient.Client, namespace, name string, renewalTime, notAfter time.Time) {
	t.Helper()
	cert := &certmanagerv1.Certificate{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(namespace, name), cert))
	renewal := metav1.NewTime(renewalTime)
	expiry := metav1.NewTime(notAfter)
	cert.Status.RenewalTime = &renewal
	cert.Status.NotAfter = &expiry
	require.NoError(t, c.Status().Update(ctx, cert))
}

// TestReplicaSetManagedCertificate_StatusReportsRenewal checks that a replica set records the next
// automatic renewal of its member certificate on the status.
func TestReplicaSetManagedCertificate_StatusReportsRenewal(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetMembers(1).SetSecurity(mdbv1.Security{
		ManagedCertificate: &mdbv1.ManagedCertificate{},
	}).Build()

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, architectures.ImageTypeUBI9, omConnectionFactory.GetConnectionFunc, nil, certs.CertManagerReadiness{Ready: true})

	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, rs.Namespace)

	memberCertName := rs.Name + "-managed-cert"
	renewalTime := time.Now().Truncate(time.Second).Add(30 * 24 * time.Hour)
	notAfter := time.Now().Truncate(time.Second).Add(90 * 24 * time.Hour)
	setCertificateRenewal(t, ctx, kubeClient, rs.Namespace, memberCertName, renewalTime, notAfter)

	_, err = reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))

	renewals := rs.Status.CertificateRenewals
	require.NotNil(t, renewals)
	require.NotNil(t, renewals.Server)
	assert.Equal(t, memberCertName, renewals.Server.Name)
	require.NotNil(t, renewals.Server.RenewalTime)
	assert.True(t, renewalTime.Equal(renewals.Server.RenewalTime.Time))
	require.NotNil(t, renewals.Server.NotAfter)
	assert.True(t, notAfter.Equal(renewals.Server.NotAfter.Time))
	assert.Nil(t, renewals.Client, "no client certificates are issued without x509 internal auth")
}

// TestMultiReplicaSetManagedCertificate_StatusReportsRenewal checks that a multi-cluster replica set
// records the next automatic renewal of its shared member certificate on the status.
func TestMultiReplicaSetManagedCertificate_StatusReportsRenewal(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetSecurity(&mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{}}).
		Build()

	reconciler, kubeClient, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs, architectures.NonStatic)

	_, err := reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	issueManagedCertificates(ctx, t, kubeClient, mrs.Namespace)

	memberCertName := mrs.Name + "-managed-cert"
	renewalTime := time.Now().Truncate(time.Second).Add(30 * 24 * time.Hour)
	notAfter := time.Now().Truncate(time.Second).Add(90 * 24 * time.Hour)
	setCertificateRenewal(t, ctx, kubeClient, mrs.Namespace, memberCertName, renewalTime, notAfter)

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(mrs), mrs))

	renewals := mrs.Status.CertificateRenewals
	require.NotNil(t, renewals)
	require.NotNil(t, renewals.Server)
	assert.Equal(t, memberCertName, renewals.Server.Name)
	require.NotNil(t, renewals.Server.RenewalTime)
	assert.True(t, renewalTime.Equal(renewals.Server.RenewalTime.Time))
	require.NotNil(t, renewals.Server.NotAfter)
	assert.True(t, notAfter.Equal(renewals.Server.NotAfter.Time))
}

// certificatesReadyCondition returns the CertificatesReady condition recorded on the resource.
func CertificatesReadyCondition(t *testing.T, rs *mdbv1.MongoDB) metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(rs.Status.Conditions, certs.ConditionCertificatesReady)
	require.NotNil(t, condition, "CertificatesReady condition is missing")
	return *condition
}
