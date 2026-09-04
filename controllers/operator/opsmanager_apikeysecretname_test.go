package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

func TestAPIKeySecretName_RejectsLegacyFallbackForDifferentNamespace(t *testing.T) {
	t.Setenv("NAMESPACE", "operator-ns")

	fakeClient := fake.NewClientBuilder().WithObjects(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shared-om-admin-key",
				Namespace: "operator-ns",
			},
			Data: map[string][]byte{
				"publicKey":  []byte("victim-legacy-key"),
				"privateKey": []byte("victim-legacy-key-priv"),
			},
		},
	).Build()

	wrappedClient := kubernetesClient.NewClient(fakeClient)
	secretClient := secrets.SecretClient{KubeClient: wrappedClient}

	om := omv1.NewOpsManagerBuilderDefault().SetName("shared-om").Build()
	om.SetNamespace("attacker-ns")

	got, err := om.APIKeySecretName(context.Background(), secretClient, "")
	require.NoError(t, err)

	// With the current code, this will FAIL — the legacy name takes precedence.
	assert.Equal(t, "attacker-ns-shared-om-admin-key", got,
		"APIKeySecretName should return namespace-qualified name, not the unqualified legacy fallback")
	assert.NotEqual(t, "shared-om-admin-key", got)
}
