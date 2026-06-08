package connectionstringsecret

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestPublishForMongoDB_ReplicaSet_UsesProvidedHostnames(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetMembers(2).Build()
	rs.Namespace = "ns-1"
	rs.UID = "rs-uid"

	c := newFakeClient(t, rs)

	hostnames := []string{
		"my-rs-0.my-rs-svc.ns-1.svc.cluster.local:27017",
		"my-rs-1.my-rs-svc.ns-1.svc.cluster.local:27017",
	}
	require.NoError(t, PublishForMongoDB(context.Background(), c, rs, hostnames))

	got := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Namespace: "ns-1", Name: "my-rs-connection-string"}, got))

	std := string(got.Data["connectionString.standard"])

	assert.True(t, strings.HasPrefix(std, "mongodb://"))
	for _, h := range hostnames {
		assert.Contains(t, std, h)
	}
	_, hasSrv := got.Data["connectionString.standardSrv"]
	assert.True(t, hasSrv, "standardSrv must be present in the secret")
	// No credentials.
	assert.NotContains(t, std, "@")
	assert.Contains(t, std, "replicaSet=my-rs",
		"replicaSet param must be present and equal the resource name")
	_, hasUsername := got.Data["username"]
	_, hasPassword := got.Data["password"]
	assert.False(t, hasUsername, "credential-less secret must not include username")
	assert.False(t, hasPassword, "credential-less secret must not include password")

	require.Len(t, got.OwnerReferences, 1)
	assert.Equal(t, "my-rs", got.OwnerReferences[0].Name)
	assert.Equal(t, types.UID("rs-uid"), got.OwnerReferences[0].UID)
}

func TestPublishForMongoDB_PassesThroughCallerHostnamesIncludingExternal(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetMembers(1).Build()
	rs.Namespace = "ns-1"

	c := newFakeClient(t, rs)
	hostnames := []string{
		"my-rs-0.my-rs-svc.ns-1.svc.cluster.local:27017",
		"vm-0.example.com:27017",
	}
	require.NoError(t, PublishForMongoDB(context.Background(), c, rs, hostnames))

	got := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Namespace: "ns-1", Name: "my-rs-connection-string"}, got))

	std := string(got.Data["connectionString.standard"])
	assert.Contains(t, std, "vm-0.example.com:27017")
	assert.Contains(t, std, "my-rs-0.my-rs-svc.ns-1.svc.cluster.local:27017")
}

func TestPublishForMongoDB_Idempotent(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetMembers(1).Build()
	rs.Namespace = "ns-1"
	c := newFakeClient(t, rs)

	hostnames := []string{"my-rs-0.my-rs-svc.ns-1.svc.cluster.local:27017"}
	require.NoError(t, PublishForMongoDB(context.Background(), c, rs, hostnames))
	require.NoError(t, PublishForMongoDB(context.Background(), c, rs, hostnames))

	list := &corev1.SecretList{}
	require.NoError(t, c.List(context.Background(), list, client.InNamespace("ns-1")))
	count := 0
	for _, s := range list.Items {
		if s.Name == "my-rs-connection-string" {
			count++
		}
	}
	assert.Equal(t, 1, count, "secret must be created exactly once across repeated calls")
}

func TestPublishForMongoDB_ReplicaSetParam_DefaultsToResourceName(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().SetName("my-rs").SetMembers(2).Build()
	rs.Namespace = "ns-1"

	c := newFakeClient(t, rs)
	hostnames := []string{
		"my-rs-0.my-rs-svc.ns-1.svc.cluster.local:27017",
		"my-rs-1.my-rs-svc.ns-1.svc.cluster.local:27017",
	}
	require.NoError(t, PublishForMongoDB(context.Background(), c, rs, hostnames))

	got := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Namespace: "ns-1", Name: "my-rs-connection-string"}, got))

	std := string(got.Data["connectionString.standard"])
	assert.Contains(t, std, "replicaSet=my-rs",
		"replicaSet param must equal the resource name when no override is set")
	assert.Equal(t, 1, strings.Count(std, "replicaSet="),
		"replicaSet param must appear exactly once")
}

func TestPublishForMongoDB_ReplicaSetParam_UsesReplicaSetNameOverride(t *testing.T) {
	rs := mdbv1.NewReplicaSetBuilder().
		SetName("my-rs").
		SetMembers(2).
		SetReplicaSetNameOverride("custom-replica-set").
		Build()
	rs.Namespace = "ns-1"

	c := newFakeClient(t, rs)
	hostnames := []string{
		"my-rs-0.my-rs-svc.ns-1.svc.cluster.local:27017",
		"my-rs-1.my-rs-svc.ns-1.svc.cluster.local:27017",
	}
	require.NoError(t, PublishForMongoDB(context.Background(), c, rs, hostnames))

	got := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Namespace: "ns-1", Name: "my-rs-connection-string"}, got))

	std := string(got.Data["connectionString.standard"])
	assert.Contains(t, std, "replicaSet=custom-replica-set",
		"replicaSet param must use ReplicaSetNameOverride when set")
	assert.NotContains(t, std, "replicaSet=my-rs",
		"replicaSet param must NOT use the Kubernetes resource name when an override is present")
}
