package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

func newFakeKube(t *testing.T) kubernetesClient.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return kubernetesClient.NewClient(fake.NewClientBuilder().WithScheme(scheme).Build())
}

func TestSelectClusterClient_FallbackWhenMembersEmpty(t *testing.T) {
	central := newFakeKube(t)
	var members map[string]kubernetesClient.Client

	got, ok := SelectClusterClient("us-east-k8s", central, members)

	assert.True(t, ok, "fallback to central should always succeed when members is empty")
	assert.Equal(t, central, got)
}

func TestSelectClusterClient_FallbackWhenClusterNameEmpty(t *testing.T) {
	central := newFakeKube(t)
	members := map[string]kubernetesClient.Client{"us-east-k8s": newFakeKube(t)}

	got, ok := SelectClusterClient("", central, members)

	assert.True(t, ok)
	assert.Equal(t, central, got, "empty clusterName means single-cluster spec.clusters[0]; fall back to central")
}

func TestSelectClusterClient_HitInMemberMap(t *testing.T) {
	central := newFakeKube(t)
	east := newFakeKube(t)
	members := map[string]kubernetesClient.Client{"us-east-k8s": east}

	got, ok := SelectClusterClient("us-east-k8s", central, members)

	assert.True(t, ok)
	assert.Equal(t, east, got)
}

func TestSelectClusterClient_MissInMemberMap(t *testing.T) {
	central := newFakeKube(t)
	east := newFakeKube(t)
	members := map[string]kubernetesClient.Client{"us-east-k8s": east}

	got, ok := SelectClusterClient("eu-west-k8s", central, members)

	assert.False(t, ok, "explicit clusterName not in member map is a configuration error, not a fallback case")
	assert.Nil(t, got)
}
