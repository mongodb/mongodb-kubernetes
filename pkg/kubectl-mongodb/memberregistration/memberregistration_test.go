package memberregistration

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
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
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mck-credential-" + memberClusterName,
			Namespace: testOperatorNamespace,
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{credentialSecretKey: ""},
	}
}

// wantMemberCluster is the MemberCluster CR Generate should emit.
func wantMemberCluster(memberClusterName, logicalName string) operatorv1.MemberCluster {
	return operatorv1.MemberCluster{
		TypeMeta: metav1.TypeMeta{APIVersion: "operator.mongodb.com/v1", Kind: "MemberCluster"},
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
			require.NotEmpty(t, gotSecret.StringData[credentialSecretKey], "credential Secret must carry a kubeconfig")
			gotSecret.StringData[credentialSecretKey] = ""

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
	rawKubeconfig, ok := secret.StringData[credentialSecretKey]
	require.True(t, ok, "credential Secret must have a %q key", credentialSecretKey)

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
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrText)
		})
	}
}
