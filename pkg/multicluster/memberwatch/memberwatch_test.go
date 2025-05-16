package memberwatch

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mc "github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
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
		assert.Equal(t, shouldAddFailedClusterAnnotation(tt.annotations, tt.clusterName), tt.out)
	}
}

func TestGetClusterCredentials(t *testing.T) {
	validCertContent := "valid-cert"
	validCert := base64.StdEncoding.EncodeToString([]byte(validCertContent))
	invalidCert := "invalid-base64!!!"
	clusterName := "cluster1"
	userToken := "abc123"
	mockUserItemList := []mc.KubeConfigUserItem{
		{Name: "user1", User: mc.KubeConfigUser{Token: userToken}},
	}
	mockKubeContext := mc.KubeConfigContextItem{
		Name: "context1",
		Context: mc.KubeConfigContext{
			Cluster: clusterName,
			User:    "user1",
		},
	}
	kubeconfigServerURL := "https://example.com"
	mockKubeConfig := mc.KubeConfigFile{
		Clusters: []mc.KubeConfigClusterItem{
			{
				Name: clusterName,
				Cluster: mc.KubeConfigCluster{
					Server:               kubeconfigServerURL,
					CertificateAuthority: validCert,
				},
			},
		},
		Users: mockUserItemList,
	}

	tests := []struct {
		name           string
		clustersMap    map[string]cluster.Cluster // Using as a set; the value is not used.
		kubeConfig     mc.KubeConfigFile
		kubeContext    mc.KubeConfigContextItem
		wantErr        bool
		errContains    string
		expectedServer string
		expectedToken  string
		expectedCA     []byte
	}{
		{
			name:        "Cluster not in clustersMap",
			clustersMap: map[string]cluster.Cluster{}, // Empty map; cluster1 is missing.
			kubeConfig:  mockKubeConfig,
			kubeContext: mockKubeContext,
			wantErr:     true,
			errContains: "cluster cluster1 not found in clustersMap",
		},
		{
			name: "Cluster missing in kubeConfig.Clusters",
			clustersMap: map[string]cluster.Cluster{
				clusterName: nil,
			},
			kubeConfig: mc.KubeConfigFile{
				Clusters: []mc.KubeConfigClusterItem{}, // No cluster defined.
				Users:    mockUserItemList,
			},
			kubeContext: mockKubeContext,
			wantErr:     true,
			errContains: "failed to get cluster with clustername: cluster1",
		},
		{
			name: "Invalid certificate authority",
			clustersMap: map[string]cluster.Cluster{
				clusterName: nil,
			},
			kubeConfig: mc.KubeConfigFile{
				Clusters: []mc.KubeConfigClusterItem{
					{
						Name: clusterName,
						Cluster: mc.KubeConfigCluster{
							Server:               kubeconfigServerURL,
							CertificateAuthority: invalidCert, // The kubeConfig has an invalid CA
						},
					},
				},
				Users: mockUserItemList,
			},
			kubeContext: mockKubeContext,
			wantErr:     true,
			errContains: "failed to decode certificate for cluster: cluster1",
		},
		{
			name: "User not found",
			clustersMap: map[string]cluster.Cluster{
				clusterName: nil,
			},
			kubeConfig: mc.KubeConfigFile{
				Clusters: []mc.KubeConfigClusterItem{
					{
						Name: clusterName,
						Cluster: mc.KubeConfigCluster{
							Server:               kubeconfigServerURL,
							CertificateAuthority: validCert,
						},
					},
				},
				Users: []mc.KubeConfigUserItem{}, // No users defined.
			},
			kubeContext: mc.KubeConfigContextItem{
				Name: "context1",
				Context: mc.KubeConfigContext{
					Cluster: clusterName,
					User:    "user1", // User is not present.
				},
			},
			wantErr:     true,
			errContains: "failed to get user with name: user1",
		},
		{
			name: "Successful extraction",
			clustersMap: map[string]cluster.Cluster{
				clusterName: nil,
			},
			kubeConfig:     mockKubeConfig,
			kubeContext:    mockKubeContext,
			wantErr:        false,
			expectedServer: kubeconfigServerURL,
			expectedToken:  userToken,
			expectedCA:     []byte(validCertContent),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			creds, err := getClusterCredentials(tc.clustersMap, tc.kubeConfig, tc.kubeContext)
			if tc.wantErr {
				assert.ErrorContains(t, err, tc.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedServer, creds.Server)
				assert.Equal(t, tc.expectedToken, creds.Token)
				assert.Equal(t, tc.expectedCA, creds.CertificateAuthority)
			}
		})
	}
}

func TestGetUserFromContext(t *testing.T) {
	tests := []struct {
		name         string
		userName     string
		users        []mc.KubeConfigUserItem
		expectedUser *mc.KubeConfigUser
	}{
		{
			name:     "User exists",
			userName: "alice",
			users: []mc.KubeConfigUserItem{
				{Name: "alice", User: mc.KubeConfigUser{Token: "alice-token"}},
				{Name: "bob", User: mc.KubeConfigUser{Token: "bob-token"}},
			},
			expectedUser: &mc.KubeConfigUser{Token: "alice-token"},
		},
		{
			name:     "User does not exist",
			userName: "charlie",
			users: []mc.KubeConfigUserItem{
				{Name: "alice", User: mc.KubeConfigUser{Token: "alice-token"}},
				{Name: "bob", User: mc.KubeConfigUser{Token: "bob-token"}},
			},
			expectedUser: nil,
		},
		{
			name:         "Empty users slice",
			userName:     "alice",
			users:        []mc.KubeConfigUserItem{},
			expectedUser: nil,
		},
		{
			name:     "Multiple users with same name, returns first match",
			userName: "duplicated",
			users: []mc.KubeConfigUserItem{
				{Name: "duplicated", User: mc.KubeConfigUser{Token: "first-token"}},
				{Name: "duplicated", User: mc.KubeConfigUser{Token: "second-token"}},
			},
			expectedUser: &mc.KubeConfigUser{Token: "first-token"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user := getUserFromContext(tc.userName, tc.users)
			assert.Equal(t, tc.expectedUser, user)
		})
	}
}
