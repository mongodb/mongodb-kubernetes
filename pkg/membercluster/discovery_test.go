package membercluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

const testNamespace = "test-ns"

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = operatorv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func kubeconfig(server string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: member
contexts:
- context:
    cluster: member
    user: mck-operator
    namespace: mongodb
  name: member
current-context: member
users:
- name: mck-operator
  user:
    token: a-token
`, server)
}

func memberClusterCR(name, clusterName, secretName string) *operatorv1.MemberCluster {
	return &operatorv1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: operatorv1.MemberClusterSpec{
			ClusterName:         clusterName,
			CredentialSecretRef: corev1.LocalObjectReference{Name: secretName},
		},
	}
}

func credentialSecret(name, server string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Data:       map[string][]byte{credentialSecretKubeconfigKey: []byte(kubeconfig(server))},
	}
}

func TestDiscover_NoCRs_FallsBack(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	restConfigs, found, err := Discover(context.Background(), c, testNamespace, 10)

	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, restConfigs)
}

func TestDiscover_BuildsMapKeyedByClusterName(t *testing.T) {
	objs := []client.Object{
		memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east"),
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
		// clusterName intentionally differs from metadata.name (e.g. non-RFC-1123 legacy name).
		memberClusterCR("cluster-west", "west_legacy", "mck-credential-cluster-west"),
		credentialSecret("mck-credential-cluster-west", "https://west.example.com:6443"),
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objs...).Build()

	restConfigs, found, err := Discover(context.Background(), c, testNamespace, 7)

	require.NoError(t, err)
	assert.True(t, found)
	require.Len(t, restConfigs, 2)

	require.Contains(t, restConfigs, "cluster-east")
	assert.Equal(t, "https://east.example.com:6443", restConfigs["cluster-east"].Host)

	// keyed by spec.clusterName, not metadata.name
	require.Contains(t, restConfigs, "west_legacy")
	assert.NotContains(t, restConfigs, "cluster-west")
	assert.Equal(t, "https://west.example.com:6443", restConfigs["west_legacy"].Host)

	// client timeout is applied
	assert.Equal(t, float64(7), restConfigs["cluster-east"].Timeout.Seconds())
}

func TestDiscover_SkipsClusterWithMissingSecret(t *testing.T) {
	objs := []client.Object{
		memberClusterCR("cluster-good", "cluster-good", "mck-credential-cluster-good"),
		credentialSecret("mck-credential-cluster-good", "https://good.example.com:6443"),
		// no credential secret for this one
		memberClusterCR("cluster-bad", "cluster-bad", "mck-credential-cluster-bad"),
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objs...).Build()

	restConfigs, found, err := Discover(context.Background(), c, testNamespace, 10)

	require.NoError(t, err)
	assert.True(t, found)
	require.Len(t, restConfigs, 1)
	assert.Contains(t, restConfigs, "cluster-good")
	assert.NotContains(t, restConfigs, "cluster-bad")
}

func TestDiscover_SkipsClusterWithSecretMissingKubeconfigKey(t *testing.T) {
	badSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mck-credential-cluster-bad", Namespace: testNamespace},
		Data:       map[string][]byte{"wrong-key": []byte("nope")},
	}
	objs := []client.Object{
		memberClusterCR("cluster-good", "cluster-good", "mck-credential-cluster-good"),
		credentialSecret("mck-credential-cluster-good", "https://good.example.com:6443"),
		memberClusterCR("cluster-bad", "cluster-bad", "mck-credential-cluster-bad"),
		badSecret,
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objs...).Build()

	restConfigs, found, err := Discover(context.Background(), c, testNamespace, 10)

	require.NoError(t, err)
	assert.True(t, found)
	require.Len(t, restConfigs, 1)
	assert.Contains(t, restConfigs, "cluster-good")
}
