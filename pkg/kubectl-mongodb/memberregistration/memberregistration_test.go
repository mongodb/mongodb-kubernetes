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
	testServiceAccount    = "mck-member-sa"
	testServerURL         = "https://api.cluster-east.example.com:6443"
	testToken             = "eyJ-test-token"
	testCA                = "test-ca-data"
)

// memberServiceAccount returns the member ServiceAccount as the user would have created it on
// the member cluster (day-2 credentials provisioning).
func memberServiceAccount(namespace string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testServiceAccount,
			Namespace: namespace,
		},
	}
}

// tokenSecret returns a ServiceAccount token Secret as the user would have created it on the
// member cluster: type kubernetes.io/service-account-token, annotated with the ServiceAccount
// name so the token controller populates it (and so Generate can discover it).
func tokenSecret(namespace, name, serviceAccount string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{corev1.ServiceAccountNameKey: serviceAccount},
		},
		Type: corev1.SecretTypeServiceAccountToken,
		Data: data,
	}
}

// populatedTokenSecret is the ready-to-use fixture: token and ca.crt present.
func populatedTokenSecret(namespace string) *corev1.Secret {
	return tokenSecret(namespace, "mck-member-token", testServiceAccount, map[string][]byte{
		corev1.ServiceAccountTokenKey:  []byte(testToken),
		corev1.ServiceAccountRootCAKey: []byte(testCA),
	})
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
			client := fake.NewSimpleClientset(memberServiceAccount(testNamespace), populatedTokenSecret(testNamespace))

			out, err := Generate(context.Background(), client, testServerURL, Options{
				MemberClusterName:           tc.memberClusterName,
				MemberClusterNamespace:      testNamespace,
				MemberClusterServiceAccount: testServiceAccount,
				OperatorNamespace:           testOperatorNamespace,
				MemberClusterLogicalName:    tc.logicalName,
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
	client := fake.NewSimpleClientset(memberServiceAccount(testNamespace), populatedTokenSecret(testNamespace))

	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:           "cluster-east",
		MemberClusterNamespace:      testNamespace,
		MemberClusterServiceAccount: testServiceAccount,
		OperatorNamespace:           testOperatorNamespace,
		MemberClusterLogicalName:    "cluster-east",
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
	client := fake.NewSimpleClientset(memberServiceAccount(testNamespace), populatedTokenSecret(testNamespace))

	const apiServerOverride = "https://member-api.internal:6443"
	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:           "cluster-east",
		MemberClusterNamespace:      testNamespace,
		MemberClusterServiceAccount: testServiceAccount,
		OperatorNamespace:           testOperatorNamespace,
		MemberClusterLogicalName:    "cluster-east",
		MemberClusterApiServer:      apiServerOverride,
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
	client := fake.NewSimpleClientset(memberServiceAccount(testNamespace), populatedTokenSecret(testNamespace))

	caOverride := generateTestCAPEM(t, "member-cluster-ca-override")
	out, err := Generate(context.Background(), client, testServerURL, Options{
		MemberClusterName:           "cluster-east",
		MemberClusterNamespace:      testNamespace,
		MemberClusterServiceAccount: testServiceAccount,
		OperatorNamespace:           testOperatorNamespace,
		MemberClusterLogicalName:    "cluster-east",
		MemberClusterApiServerCA:    caOverride,
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
		objects        []runtime.Object
		serviceAccount string
		wantErrTexts   []string
	}{
		"missing service account": {
			objects:      nil,
			wantErrTexts: []string{"reading ServiceAccount mongodb/mck-member-sa"},
		},
		"missing token secret": {
			objects:      []runtime.Object{memberServiceAccount(testNamespace)},
			wantErrTexts: []string{`no token Secret for ServiceAccount "mck-member-sa" found`},
		},
		"annotated secret of a different type is ignored": {
			objects: []runtime.Object{
				memberServiceAccount(testNamespace),
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "not-a-token-secret",
						Namespace:   testNamespace,
						Annotations: map[string]string{corev1.ServiceAccountNameKey: testServiceAccount},
					},
					Type: corev1.SecretTypeOpaque,
				},
			},
			wantErrTexts: []string{`no token Secret for ServiceAccount "mck-member-sa" found`},
		},
		"token secret annotated for another service account": {
			objects: []runtime.Object{
				memberServiceAccount(testNamespace),
				tokenSecret(testNamespace, "other-token", "somebody-else", map[string][]byte{
					corev1.ServiceAccountTokenKey:  []byte(testToken),
					corev1.ServiceAccountRootCAKey: []byte(testCA),
				}),
			},
			wantErrTexts: []string{`no token Secret for ServiceAccount "mck-member-sa" found`},
		},
		"multiple token secrets for the service account": {
			objects: []runtime.Object{
				memberServiceAccount(testNamespace),
				tokenSecret(testNamespace, "token-one", testServiceAccount, nil),
				tokenSecret(testNamespace, "token-two", testServiceAccount, nil),
			},
			// The ambiguity error must name the candidates so the user knows what to delete.
			wantErrTexts: []string{"found 2 token Secrets", "token-one", "token-two"},
		},
		"empty service account name": {
			objects:        []runtime.Object{memberServiceAccount(testNamespace), populatedTokenSecret(testNamespace)},
			serviceAccount: "  ",
			wantErrTexts:   []string{"MemberClusterServiceAccount must be non-empty"},
		},
		"missing token key": {
			objects: []runtime.Object{
				memberServiceAccount(testNamespace),
				tokenSecret(testNamespace, "mck-member-token", testServiceAccount, map[string][]byte{
					corev1.ServiceAccountRootCAKey: []byte(testCA),
				}),
			},
			wantErrTexts: []string{`has no "token" key`},
		},
		"missing ca key": {
			objects: []runtime.Object{
				memberServiceAccount(testNamespace),
				tokenSecret(testNamespace, "mck-member-token", testServiceAccount, map[string][]byte{
					corev1.ServiceAccountTokenKey: []byte(testToken),
				}),
			},
			wantErrTexts: []string{`has no "ca.crt" key`},
		},
	}

	originalPollInterval := tokenPollInterval
	tokenPollInterval = time.Millisecond
	defer func() { tokenPollInterval = originalPollInterval }()

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tc.objects...)

			serviceAccount := tc.serviceAccount
			if serviceAccount == "" {
				serviceAccount = testServiceAccount
			}

			_, err := Generate(context.Background(), client, testServerURL, Options{
				MemberClusterName:           "cluster-east",
				MemberClusterNamespace:      testNamespace,
				MemberClusterServiceAccount: serviceAccount,
				OperatorNamespace:           testOperatorNamespace,
				MemberClusterLogicalName:    "cluster-east",
				TokenWaitTimeout:            50 * time.Millisecond,
			})
			require.Error(t, err)
			for _, want := range tc.wantErrTexts {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestGenerate_WaitsForTokenPopulation(t *testing.T) {
	originalPollInterval := tokenPollInterval
	tokenPollInterval = time.Millisecond
	defer func() { tokenPollInterval = originalPollInterval }()

	populated := populatedTokenSecret(testNamespace)
	client := fake.NewSimpleClientset(memberServiceAccount(testNamespace), populated)

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
		MemberClusterName:           "cluster-east",
		MemberClusterNamespace:      testNamespace,
		MemberClusterServiceAccount: testServiceAccount,
		OperatorNamespace:           testOperatorNamespace,
		MemberClusterLogicalName:    "cluster-east",
		TokenWaitTimeout:            10 * time.Second,
	})
	require.NoError(t, err)

	gotSecret, gotMemberCluster := parseOutput(t, out)
	assert.Equal(t, "mck-credential-cluster-east", gotSecret.Name)
	assert.Equal(t, "cluster-east", gotMemberCluster.Name)
}

// The token Secret must already exist (its absence is an immediate error — see
// TestGenerate_Errors); only its population by the token controller is awaited.
func TestGenerate_TokenWaitTimeout(t *testing.T) {
	tests := map[string]struct {
		objects     []runtime.Object
		wantErrText []string
	}{
		"secret never populated": {
			objects: []runtime.Object{
				memberServiceAccount(testNamespace),
				tokenSecret(testNamespace, "mck-member-token", testServiceAccount, nil),
			},
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
			client := fake.NewSimpleClientset(tc.objects...)

			_, err := Generate(context.Background(), client, testServerURL, Options{
				MemberClusterName:           "cluster-east",
				MemberClusterNamespace:      testNamespace,
				MemberClusterServiceAccount: testServiceAccount,
				OperatorNamespace:           testOperatorNamespace,
				MemberClusterLogicalName:    "cluster-east",
				TokenWaitTimeout:            50 * time.Millisecond,
			})
			require.Error(t, err)
			for _, want := range tc.wantErrText {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
