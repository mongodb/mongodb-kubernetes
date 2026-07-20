package memberregistration

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	testNamespace = "mongodb"
	testServerURL = "https://api.cluster-east.example.com:6443"
	testToken     = "eyJ-test-token"
	testCA        = "test-ca-data"
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

// parseResources decodes a multi-document YAML manifest into unstructured objects.
func parseResources(t *testing.T, manifest string) []*unstructured.Unstructured {
	t.Helper()
	var out []*unstructured.Unstructured
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		obj := &unstructured.Unstructured{}
		err := dec.Decode(obj)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "failed to parse rendered manifest")
		if obj.GetKind() == "" {
			continue
		}
		out = append(out, obj)
	}
	return out
}

func findByKind(rs []*unstructured.Unstructured, kind string) *unstructured.Unstructured {
	for _, r := range rs {
		if r.GetKind() == kind {
			return r
		}
	}
	return nil
}

func TestGenerate(t *testing.T) {
	const operatorNamespace = "mongodb-operator"

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
				OperatorNamespace:        operatorNamespace,
				MemberClusterLogicalName: tc.logicalName,
			})
			require.NoError(t, err)

			resources := parseResources(t, out)

			// Exactly a credential Secret and a MemberCluster CR, in that order.
			require.Len(t, resources, 2)
			assert.Equal(t, "Secret", resources[0].GetKind(), "credential Secret must come first")
			assert.Equal(t, "MemberCluster", resources[1].GetKind())

			wantCredentialSecretName := "mck-credential-" + tc.memberClusterName

			secret := findByKind(resources, "Secret")
			require.NotNil(t, secret)
			assert.Equal(t, wantCredentialSecretName, secret.GetName())
			assert.Equal(t, operatorNamespace, secret.GetNamespace())
			assert.Equal(t, "v1", secret.GetAPIVersion())
			secretType, _, _ := unstructured.NestedString(secret.Object, "type")
			assert.Equal(t, string(corev1.SecretTypeOpaque), secretType)

			mc := findByKind(resources, "MemberCluster")
			require.NotNil(t, mc)
			assert.Equal(t, tc.memberClusterName, mc.GetName(), "metadata.name comes from MemberClusterName")
			assert.Equal(t, operatorNamespace, mc.GetNamespace())
			assert.Equal(t, "operator.mongodb.com/v1", mc.GetAPIVersion())
			clusterName, _, _ := unstructured.NestedString(mc.Object, "spec", "clusterName")
			assert.Equal(t, tc.logicalName, clusterName, "spec.clusterName comes from MemberClusterLogicalName")
			credRef, _, _ := unstructured.NestedString(mc.Object, "spec", "credentialSecretRef", "name")
			assert.Equal(t, wantCredentialSecretName, credRef, "MemberCluster must reference the credential Secret")
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
		OperatorNamespace:        "mongodb-operator",
		MemberClusterLogicalName: "cluster-east",
	})
	require.NoError(t, err)

	secret := findByKind(parseResources(t, out), "Secret")
	require.NotNil(t, secret)
	stringData, _, err := unstructured.NestedStringMap(secret.Object, "stringData")
	require.NoError(t, err)
	rawKubeconfig, ok := stringData[credentialSecretKey]
	require.True(t, ok, "credential Secret must have a %q key", credentialSecretKey)

	cfg, err := clientcmd.Load([]byte(rawKubeconfig))
	require.NoError(t, err)

	// Single-context kubeconfig with the extracted server, CA and bearer token.
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
				OperatorNamespace:        "mongodb-operator",
				MemberClusterLogicalName: "cluster-east",
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrText)
		})
	}
}
