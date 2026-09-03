package memberregistration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"

	cryptorand "crypto/rand"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	clienttesting "k8s.io/client-go/testing"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	testNamespace         = "mongodb"
	testOperatorNamespace = "mongodb-operator"
	testServerURL         = "https://api.cluster-east.example.com:6443"
	testToken             = "eyJ-test-token"
	testCA                = "test-ca-data"
)

// tokenSecret returns a ServiceAccount token Secret as generate-member-resources would have
// created it on the member cluster, keyed by cluster name.
func tokenSecret(clusterName, namespace string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mck-member-" + clusterName + "-token",
			Namespace: namespace,
		},
		Type: corev1.SecretTypeServiceAccountToken,
		Data: data,
	}
}

// parseOutput decodes Generate's output into the two typed docs it emits, in order: Secret, then MemberCluster.
func parseOutput(t *testing.T, manifest string) (corev1.Secret, operatorv1.MemberCluster) {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)

	var secret corev1.Secret
	require.NoError(t, dec.Decode(&secret), "decoding the first document as a Secret")
	require.Equal(t, "Secret", secret.Kind, "credential Secret must be the first document")

	var memberCluster operatorv1.MemberCluster
	require.NoError(t, dec.Decode(&memberCluster), "decoding the second document as a MemberCluster")

	require.ErrorIs(t, dec.Decode(new(struct{})), io.EOF, "expected exactly two documents")
	return secret, memberCluster
}

// wantCredentialSecret is the Secret Generate should emit. The kubeconfig payload is blanked here
// and checked in TestGenerate_KubeconfigContents.
func wantCredentialSecret(memberClusterName string) corev1.Secret {
	return corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mck-credential-" + memberClusterName,
			Namespace: testOperatorNamespace,
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{util.MemberClusterCredentialSecretKubeconfigKey: ""},
	}
}

// wantMemberCluster is the MemberCluster CR Generate should emit.
func wantMemberCluster(memberClusterName, logicalName string) operatorv1.MemberCluster {
	return operatorv1.MemberCluster{
		TypeMeta: metav1.TypeMeta{APIVersion: operatorv1.GroupVersion.String(), Kind: "MemberCluster"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      memberClusterName,
			Namespace: testOperatorNamespace,
		},
		Spec: operatorv1.MemberClusterSpec{
			ClusterName:         logicalName,
			CredentialSecretRef: corev1.LocalObjectReference{Name: "mck-credential-" + memberClusterName},
		},
	}
}

func TestGenerate(t *testing.T) {
	tests := map[string]struct {
		memberClusterName string
		logicalName       string
	}{
		"logical name matches member cluster name": {
			memberClusterName: "cluster-east",
			logicalName:       "cluster-east",
		},
		// metadata.name (member cluster name) is RFC 1123 compliant; the logical name differs
		// (e.g. an MCK 1.x name with an underscore that must not be modified).
		"logical name differs from member cluster name": {
			memberClusterName: "cluster-legacy",
			logicalName:       "legacy_cluster_name",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tokenSecret(tc.memberClusterName, testNamespace, map[string][]byte{
				corev1.ServiceAccountTokenKey:  []byte(testToken),
				corev1.ServiceAccountRootCAKey: []byte(testCA),
			}))

			out, err := Generate(context.Background(), client, testServerURL, Options{
				MemberClusterName:        tc.memberClusterName,
				MemberClusterNamespace:   testNamespace,
				OperatorNamespace:        testOperatorNamespace,
				MemberClusterLogicalName: tc.logicalName,
			})
			require.NoError(t, err)

			gotSecret, gotMemberCluster := parseOutput(t, out)

			// Contents checked in TestGenerate_KubeconfigContents; here just require it present, then blank for the compare.
			require.NotEmpty(t, gotSecret.StringData[util.MemberClusterCredentialSecretKubeconfigKey], "credential Secret must carry a kubeconfig")
			gotSecret.StringData[util.MemberClusterCredentialSecretKubeconfigKey] = ""

			assert.Equal(t, wantCredentialSecret(tc.memberClusterName), gotSecret)
			assert.Equal(t, wantMemberCluster(tc.memberClusterName, tc.logicalName), gotMemberCluster)
		})
	}
}

func TestGenerate_KubeconfigContents(t *testing.T) {
	client := fake.NewSimpleClientset(tokenSecret("cluster-east", testNamespace, map[string][]byte{
		corev1.ServiceAccountTokenKey:  []byte(testToken),
		corev1.ServiceAccountRootCAKey: []byte(testCA),
	}))

	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:        "cluster-east",
		MemberClusterNamespace:   testNamespace,
		OperatorNamespace:        testOperatorNamespace,
		MemberClusterLogicalName: "cluster-east",
	})
	require.NoError(t, err)

	secret, _ := parseOutput(t, out)
	rawKubeconfig, ok := secret.StringData[util.MemberClusterCredentialSecretKubeconfigKey]
	require.True(t, ok, "credential Secret must have a %q key", util.MemberClusterCredentialSecretKubeconfigKey)

	cfg, err := clientcmd.Load([]byte(rawKubeconfig))
	require.NoError(t, err)

	// Single-context kubeconfig.
	require.Len(t, cfg.Clusters, 1)
	require.Len(t, cfg.Contexts, 1)
	require.Len(t, cfg.AuthInfos, 1)
	require.NotEmpty(t, cfg.CurrentContext)

	currentCtx := cfg.Contexts[cfg.CurrentContext]
	require.NotNil(t, currentCtx)
	cluster := cfg.Clusters[currentCtx.Cluster]
	require.NotNil(t, cluster)
	assert.Equal(t, testServerURL, cluster.Server)
	assert.Equal(t, []byte(testCA), cluster.CertificateAuthorityData)
	assert.Equal(t, testNamespace, currentCtx.Namespace)

	authInfo := cfg.AuthInfos[currentCtx.AuthInfo]
	require.NotNil(t, authInfo)
	assert.Equal(t, testToken, authInfo.Token)
}

func TestGenerate_KubeconfigContents_ApiServerOverride(t *testing.T) {
	client := fake.NewSimpleClientset(tokenSecret("cluster-east", testNamespace, map[string][]byte{
		corev1.ServiceAccountTokenKey:  []byte(testToken),
		corev1.ServiceAccountRootCAKey: []byte(testCA),
	}))

	const apiServerOverride = "https://member-api.internal:6443"
	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:        "cluster-east",
		MemberClusterNamespace:   testNamespace,
		OperatorNamespace:        testOperatorNamespace,
		MemberClusterLogicalName: "cluster-east",
		MemberClusterApiServer:   apiServerOverride,
	})
	require.NoError(t, err)

	secret, _ := parseOutput(t, out)
	cfg, err := clientcmd.Load([]byte(secret.StringData[util.MemberClusterCredentialSecretKubeconfigKey]))
	require.NoError(t, err)

	cluster := cfg.Clusters[cfg.Contexts[cfg.CurrentContext].Cluster]
	require.NotNil(t, cluster)
	assert.Equal(t, apiServerOverride, cluster.Server)
}

func TestGenerate_KubeconfigContents_CertificateAuthorityOverride(t *testing.T) {
	client := fake.NewSimpleClientset(tokenSecret("cluster-east", testNamespace, map[string][]byte{
		corev1.ServiceAccountTokenKey:  []byte(testToken),
		corev1.ServiceAccountRootCAKey: []byte(testCA),
	}))

	caOverride := generateTestCAPEM(t, "member-cluster-ca-override")
	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:        "cluster-east",
		MemberClusterNamespace:   testNamespace,
		OperatorNamespace:        testOperatorNamespace,
		MemberClusterLogicalName: "cluster-east",
		MemberClusterApiServerCA: caOverride,
	})
	require.NoError(t, err)

	secret, _ := parseOutput(t, out)
	cfg, err := clientcmd.Load([]byte(secret.StringData[util.MemberClusterCredentialSecretKubeconfigKey]))
	require.NoError(t, err)

	cluster := cfg.Clusters[cfg.Contexts[cfg.CurrentContext].Cluster]
	require.NotNil(t, cluster)
	assert.Equal(t, caOverride, cluster.CertificateAuthorityData)
}

// generateTestCAPEM returns a self signed, PEM encoded CA certificate. It is generated rather than
// hardcoded so the fixture is always accepted by x509.CertPool.AppendCertsFromPEM, which is what
// the CLI's --member-cluster-api-server-ca validation relies on.
func generateTestCAPEM(t *testing.T, commonName string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour * 24),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(cryptorand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestGenerate_Errors(t *testing.T) {
	tests := map[string]struct {
		objects     []*corev1.Secret
		wantErrText string
	}{
		"missing token secret": {
			objects:     nil,
			wantErrText: "reading token secret",
		},
		"missing token key": {
			objects: []*corev1.Secret{tokenSecret("cluster-east", testNamespace, map[string][]byte{
				corev1.ServiceAccountRootCAKey: []byte(testCA),
			})},
			wantErrText: `has no "token" key`,
		},
		"missing ca key": {
			objects: []*corev1.Secret{tokenSecret("cluster-east", testNamespace, map[string][]byte{
				corev1.ServiceAccountTokenKey: []byte(testToken),
			})},
			wantErrText: `has no "ca.crt" key`,
		},
	}

	originalPollInterval := tokenPollInterval
	tokenPollInterval = time.Millisecond
	defer func() { tokenPollInterval = originalPollInterval }()

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, o := range tc.objects {
				_, err := client.CoreV1().Secrets(o.Namespace).Create(context.Background(), o, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			_, err := Generate(context.Background(), client, testServerURL, Options{
				MemberClusterName:        "cluster-east",
				MemberClusterNamespace:   testNamespace,
				OperatorNamespace:        testOperatorNamespace,
				MemberClusterLogicalName: "cluster-east",
				TokenWaitTimeout:         50 * time.Millisecond,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrText)
		})
	}
}

func TestGenerate_WaitsForTokenPopulation(t *testing.T) {
	originalPollInterval := tokenPollInterval
	tokenPollInterval = time.Millisecond
	defer func() { tokenPollInterval = originalPollInterval }()

	populated := tokenSecret("cluster-east", testNamespace, map[string][]byte{
		corev1.ServiceAccountTokenKey:  []byte(testToken),
		corev1.ServiceAccountRootCAKey: []byte(testCA),
	})
	client := fake.NewSimpleClientset(populated)

	// Simulate Kubernetes's token controller being slow: the first two reads see the Secret with
	// no data, subsequent reads fall through to the tracker (which holds the populated Secret).
	getCalls := 0
	client.PrependReactor("get", "secrets", func(_ clienttesting.Action) (bool, runtime.Object, error) {
		getCalls++
		if getCalls <= 2 {
			empty := populated.DeepCopy()
			empty.Data = nil
			return true, empty, nil
		}
		return false, nil, nil
	})

	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:        "cluster-east",
		MemberClusterNamespace:   testNamespace,
		OperatorNamespace:        testOperatorNamespace,
		MemberClusterLogicalName: "cluster-east",
		TokenWaitTimeout:         10 * time.Second,
	})
	require.NoError(t, err)

	gotSecret, gotMemberCluster := parseOutput(t, out)
	assert.Equal(t, "mck-credential-cluster-east", gotSecret.Name)
	assert.Equal(t, "cluster-east", gotMemberCluster.Name)
}

func TestGenerate_TokenWaitTimeout(t *testing.T) {
	tests := map[string]struct {
		objects     []*corev1.Secret
		wantErrText []string
	}{
		"secret never created": {
			objects: nil,
			wantErrText: []string{
				"timed out after 50ms",
				"was 'generate-member-resources' applied to it?",
			},
		},
		"secret never populated": {
			objects: []*corev1.Secret{tokenSecret("cluster-east", testNamespace, nil)},
			wantErrText: []string{
				"timed out after 50ms",
				`has no "token" key`,
			},
		},
	}

	originalPollInterval := tokenPollInterval
	tokenPollInterval = time.Millisecond
	defer func() { tokenPollInterval = originalPollInterval }()

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, o := range tc.objects {
				_, err := client.CoreV1().Secrets(o.Namespace).Create(context.Background(), o, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			_, err := Generate(context.Background(), client, testServerURL, Options{
				MemberClusterName:        "cluster-east",
				MemberClusterNamespace:   testNamespace,
				OperatorNamespace:        testOperatorNamespace,
				MemberClusterLogicalName: "cluster-east",
				TokenWaitTimeout:         50 * time.Millisecond,
			})
			require.Error(t, err)
			for _, want := range tc.wantErrText {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
