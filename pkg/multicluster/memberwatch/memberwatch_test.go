package memberwatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"

	apiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster/failedcluster"
)

func TestClusterWithMinimumNumber(t *testing.T) {
	tests := []struct {
		inp mdb.ClusterSpecList
		out int
	}{
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			out: 1,
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 1},
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 3},
				{ClusterName: "cluster4", Members: 4},
			},
			out: 0,
		},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.out, clusterWithMinimumMembers(tt.inp))
	}
}

func TestDistributeFailedMembers(t *testing.T) {
	tests := []struct {
		inp         mdb.ClusterSpecList
		clusterName string
		out         mdb.ClusterSpecList
	}{
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster1",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster2",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster3",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 3},
				{ClusterName: "cluster2", Members: 3},
				{ClusterName: "cluster4", Members: 2},
			},
		},
		{
			inp: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 1},
				{ClusterName: "cluster3", Members: 4},
				{ClusterName: "cluster4", Members: 1},
			},
			clusterName: "cluster4",
			out: mdb.ClusterSpecList{
				{ClusterName: "cluster1", Members: 2},
				{ClusterName: "cluster2", Members: 2},
				{ClusterName: "cluster3", Members: 4},
			},
		},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.out, distributeFailedMembers(tt.inp, tt.clusterName))
	}
}

func getFailedClusterList(clusters []string) string {
	failedClusters := make([]failedcluster.FailedCluster, len(clusters))

	for n, c := range clusters {
		failedClusters[n] = failedcluster.FailedCluster{ClusterName: c, Members: 2}
	}

	failedClusterBytes, _ := json.Marshal(failedClusters)
	return string(failedClusterBytes)
}

func TestShouldAddFailedClusterAnnotation(t *testing.T) {
	tests := []struct {
		annotations map[string]string
		clusterName string
		out         bool
	}{
		{
			annotations: nil,
			clusterName: "cluster1",
			out:         true,
		},
		{
			annotations: map[string]string{failedcluster.FailedClusterAnnotation: getFailedClusterList([]string{"cluster1", "cluster2"})},
			clusterName: "cluster1",
			out:         false,
		},
		{
			annotations: map[string]string{failedcluster.FailedClusterAnnotation: getFailedClusterList([]string{"cluster1", "cluster2", "cluster4"})},
			clusterName: "cluster3",
			out:         true,
		},
	}

	for _, tt := range tests {
		assert.Equal(t, !isInFailedClusterAnnotation(tt.annotations, tt.clusterName), tt.out)
	}
}

func TestCredentialsFromRestConfig(t *testing.T) {
	t.Run("inline CA data and bearer token", func(t *testing.T) {
		restConfig := &restclient.Config{
			Host:        "https://cluster-east.example.com:6443",
			BearerToken: "inline-token",
			TLSClientConfig: restclient.TLSClientConfig{
				CAData: []byte("inline-ca"),
			},
		}

		creds, err := credentialsFromRestConfig(restConfig)

		require.NoError(t, err)
		assert.Equal(t, "https://cluster-east.example.com:6443", creds.Server)
		assert.Equal(t, "inline-token", creds.Token)
		assert.Equal(t, []byte("inline-ca"), creds.CertificateAuthority)
	})

	t.Run("falls back to CA and token files", func(t *testing.T) {
		dir := t.TempDir()
		caFile := filepath.Join(dir, "ca.crt")
		tokenFile := filepath.Join(dir, "token")
		require.NoError(t, os.WriteFile(caFile, []byte("file-ca"), 0o600))
		require.NoError(t, os.WriteFile(tokenFile, []byte("file-token"), 0o600))

		restConfig := &restclient.Config{
			Host:            "https://cluster-west.example.com:6443",
			BearerTokenFile: tokenFile,
			TLSClientConfig: restclient.TLSClientConfig{
				CAFile: caFile,
			},
		}

		creds, err := credentialsFromRestConfig(restConfig)

		require.NoError(t, err)
		assert.Equal(t, "https://cluster-west.example.com:6443", creds.Server)
		assert.Equal(t, "file-token", creds.Token)
		assert.Equal(t, []byte("file-ca"), creds.CertificateAuthority)
	})

	t.Run("inline data takes precedence over files", func(t *testing.T) {
		restConfig := &restclient.Config{
			Host:            "https://cluster.example.com:6443",
			BearerToken:     "inline-token",
			BearerTokenFile: "/does/not/exist/token",
			TLSClientConfig: restclient.TLSClientConfig{
				CAData: []byte("inline-ca"),
				CAFile: "/does/not/exist/ca",
			},
		}

		creds, err := credentialsFromRestConfig(restConfig)

		require.NoError(t, err)
		assert.Equal(t, "inline-token", creds.Token)
		assert.Equal(t, []byte("inline-ca"), creds.CertificateAuthority)
	})
}

func TestAddAndRemoveFailedClusterAnnotation(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	mrs := &mdbmulti.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdbmc",
			Namespace: "ns",
		},
		Spec: mdbmulti.MongoDBMultiSpec{},
	}
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{
		{ClusterName: "cluster1", Members: 2},
		{ClusterName: "cluster2", Members: 2},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mrs).Build()
	central := kubernetesClient.NewClient(fakeClient)

	// 1. Initial state: no failedClusters annotation.
	got := &mdbmulti.MongoDBMultiCluster{}
	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
	_, present := got.Annotations[failedcluster.FailedClusterAnnotation]
	require.False(t, present, "precondition: annotation should not exist yet")

	// 2. Mark cluster1 as failed; annotation should appear with cluster1 in it.
	require.NoError(t, addFailedClustersAnnotation(ctx, *mrs, "cluster1", central))

	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
	val, present := got.Annotations[failedcluster.FailedClusterAnnotation]
	require.True(t, present, "annotation should exist after adding cluster1")
	assert.True(t, isInFailedClusterAnnotation(got.Annotations, "cluster1"))

	var parsed []failedcluster.FailedCluster
	require.NoError(t, json.Unmarshal([]byte(val), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "cluster1", parsed[0].ClusterName)

	// 3. Remove cluster1; the annotation key should be deleted entirely.
	require.NoError(t, removeClusterFromFailedAnnotation(ctx, *got, "cluster1", central))

	require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
	_, present = got.Annotations[failedcluster.FailedClusterAnnotation]
	assert.False(t, present, "annotation key should be removed when the list becomes empty")
}
