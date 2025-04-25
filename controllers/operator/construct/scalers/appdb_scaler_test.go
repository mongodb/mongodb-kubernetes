package scalers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

func TestAppDBMultiClusterScaler(t *testing.T) {
	testCases := []struct {
		name                               string
		memberClusterName                  string
		memberClusterNum                   int
		clusterSpecList                    mdbv1.ClusterSpecList
		prevMembers                        []multicluster.MemberCluster
		expectedDesiredReplicas            int
		expectedCurrentReplicas            int
		expectedReplicasThisReconciliation int
	}{
		{
			name:              "no previous members",
			memberClusterName: "cluster-1",
			memberClusterNum:  0,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     2,
				},
				{
					ClusterName: "cluster-3",
					Members:     2,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 0},
				{Name: "cluster-2", Index: 1, Replicas: 0},
				{Name: "cluster-3", Index: 2, Replicas: 0},
			},
			expectedDesiredReplicas:            3,
			expectedCurrentReplicas:            0,
			expectedReplicasThisReconciliation: 3,
		},
		{
			name:              "scaling up one member",
			memberClusterName: "cluster-1",
			memberClusterNum:  0,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     2,
				},
				{
					ClusterName: "cluster-3",
					Members:     2,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 1},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            3,
			expectedCurrentReplicas:            1,
			expectedReplicasThisReconciliation: 2,
		},
		{
			name:              "scaling down one member",
			memberClusterName: "cluster-2",
			memberClusterNum:  1,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     0,
				},
				{
					ClusterName: "cluster-3",
					Members:     2,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            0,
			expectedCurrentReplicas:            2,
			expectedReplicasThisReconciliation: 1,
		},
		{
			name:              "scaling up multiple members cluster currently scaling",
			memberClusterName: "cluster-2",
			memberClusterNum:  1,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     4,
				},
				{
					ClusterName: "cluster-3",
					Members:     3,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            4,
			expectedCurrentReplicas:            2,
			expectedReplicasThisReconciliation: 3,
		},
		{
			name:              "scaling up multiple members cluster currently not scaling",
			memberClusterName: "cluster-3",
			memberClusterNum:  2,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     4,
				},
				{
					ClusterName: "cluster-3",
					Members:     3,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            2,
			expectedCurrentReplicas:            2,
			expectedReplicasThisReconciliation: 2,
		},
		{
			name:              "scaling down multiple members cluster currently scaling",
			memberClusterName: "cluster-2",
			memberClusterNum:  1,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     0,
				},
				{
					ClusterName: "cluster-3",
					Members:     0,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            0,
			expectedCurrentReplicas:            2,
			expectedReplicasThisReconciliation: 1,
		},
		{
			name:              "scaling down multiple members cluster currently not scaling",
			memberClusterName: "cluster-3",
			memberClusterNum:  2,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     0,
				},
				{
					ClusterName: "cluster-3",
					Members:     0,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            2,
			expectedCurrentReplicas:            2,
			expectedReplicasThisReconciliation: 2,
		},
		{
			name:              "no scaling required",
			memberClusterName: "cluster-3",
			memberClusterNum:  2,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     2,
				},
				{
					ClusterName: "cluster-3",
					Members:     2,
				},
			},
			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
			},
			expectedDesiredReplicas:            2,
			expectedCurrentReplicas:            2,
			expectedReplicasThisReconciliation: 2,
		},
		{
			name:              "adding a new cluster to an already populated config",
			memberClusterName: "cluster-4",
			memberClusterNum:  3,
			clusterSpecList: mdbv1.ClusterSpecList{
				{
					ClusterName: "cluster-1",
					Members:     3,
				},
				{
					ClusterName: "cluster-2",
					Members:     2,
				},
				{
					ClusterName: "cluster-3",
					Members:     2,
				},
				{
					ClusterName: "cluster-4",
					Members:     3,
				},
			},

			prevMembers: []multicluster.MemberCluster{
				{Name: "cluster-1", Index: 0, Replicas: 3},
				{Name: "cluster-2", Index: 1, Replicas: 2},
				{Name: "cluster-3", Index: 2, Replicas: 2},
				{Name: "cluster-4", Index: 3, Replicas: 0},
			},

			expectedDesiredReplicas:            3,
			expectedCurrentReplicas:            0,
			expectedReplicasThisReconciliation: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			builder := opsManagerBuilder().SetAppDBTopology(mdbv1.ClusterTopologyMultiCluster)
			opsManager := builder.SetAppDBClusterSpecList(tc.clusterSpecList).Build()
			scaler := GetAppDBScaler(opsManager, tc.memberClusterName, tc.memberClusterNum, tc.prevMembers)

			assert.Equal(t, tc.expectedDesiredReplicas, scaler.DesiredReplicas(), "Desired replicas")
			assert.Equal(t, tc.expectedCurrentReplicas, scaler.CurrentReplicas(), "Current replicas")
			assert.Equal(t, tc.expectedReplicasThisReconciliation, scale.ReplicasThisReconciliation(scaler), "Replicas this reconciliation")
		})
	}
}

func opsManagerBuilder() *omv1.OpsManagerBuilder {
	spec := omv1.MongoDBOpsManagerSpec{
		Version:     "5.0.0",
		AppDB:       *omv1.DefaultAppDbBuilder().Build(),
		AdminSecret: "om-admin",
	}
	resource := omv1.MongoDBOpsManager{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "test-om", Namespace: mock.TestNamespace}}
	return omv1.NewOpsManagerBuilderFromResource(resource)
}
