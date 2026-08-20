package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func TestReadDirectiveViews(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	directive := buildDirective(m, clusters[0], 1, "some-hash")

	clientsMap := map[string]kubernetesClient.Client{
		clusters[0]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithObjects(directive).Build()),
		clusters[1]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build()),
		clusters[2]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return xerrors.New("cluster unreachable")
			},
		}).Build()),
	}

	views := readDirectiveViews(ctx, clientsMap, requestFromObject(m).NamespacedName, zap.S())

	require.Len(t, views, 3)
	assert.True(t, views[clusters[0]].Exists)
	assert.Equal(t, directive.Spec, views[clusters[0]].Spec)
	assert.False(t, views[clusters[1]].Exists)
	assert.False(t, views[clusters[1]].Unreachable)
	assert.True(t, views[clusters[2]].Unreachable)
	assert.False(t, views[clusters[2]].Exists)
}

func TestClusterIndexFromProcessName(t *testing.T) {
	cases := []struct {
		processName string
		rsName      string
		wantIndex   int
		wantOK      bool
	}{
		{"temple-0-0", "temple", 0, true},
		{"temple-2-14", "temple", 2, true},
		{"my-rs-7-1-0", "my-rs-7", 1, true}, // dashed, digit-carrying CR name stays unambiguous
		{"other-0-0", "temple", 0, false},   // foreign replica set
		{"temple-0", "temple", 0, false},    // single-cluster naming, not ours
		{"temple-x-0", "temple", 0, false},
	}
	for _, tc := range cases {
		idx, ok := clusterIndexFromProcessName(tc.processName, tc.rsName)
		assert.Equal(t, tc.wantOK, ok, tc.processName)
		if tc.wantOK {
			assert.Equal(t, tc.wantIndex, idx, tc.processName)
		}
	}
}

func TestACViewFromDeployment(t *testing.T) {
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	counts := []process.ClusterProcessCount{
		{ClusterName: clusters[0], ClusterIndex: 0, MemberCount: 2},
		{ClusterName: clusters[1], ClusterIndex: 1, MemberCount: 1},
	}
	processes := process.CreateMongodProcessesMultiFromCounts("", false, *m, counts, "", architectures.NonStatic)
	rs := om.NewMultiClusterReplicaSetWithProcesses(om.NewReplicaSet(m.Name, m.Spec.Version), processes, nil, map[string]int{}, nil)

	deployment := om.NewDeployment()
	deployment.MergeReplicaSet(rs, nil, nil, zap.S())
	deployment.SetOperatorLeadershipTerm(4)

	view := acViewFromDeployment(deployment, m.Name)

	assert.True(t, view.Read)
	assert.Equal(t, int64(4), view.LeadershipTerm)
	assert.Equal(t, map[int]int{0: 2, 1: 1}, view.MemberCountsByIndex)
}

func TestACViewFromEmptyDeployment(t *testing.T) {
	view := acViewFromDeployment(om.NewDeployment(), "temple")
	assert.True(t, view.Read)
	assert.Equal(t, int64(0), view.LeadershipTerm)
	assert.Empty(t, view.MemberCountsByIndex)
}

func TestOMFactsFromClusterState(t *testing.T) {
	now := time.Now()
	state := agents.MongoDBClusterStateInOM{
		GoalVersion: 2,
		ProcessStateMap: map[string]agents.ProcessState{
			"fresh-in-goal":  {Hostname: "fresh-in-goal", LastAgentPing: now, GoalVersionAchieved: 2},
			"fresh-behind":   {Hostname: "fresh-behind", LastAgentPing: now, GoalVersionAchieved: 1},
			"stale-in-goal":  {Hostname: "stale-in-goal", LastAgentPing: now.Add(-agents.StaleProcessDuration - time.Minute), GoalVersionAchieved: 2},
			"never-achieved": {Hostname: "never-achieved", LastAgentPing: now, GoalVersionAchieved: -1},
		},
	}

	view := omFactsFromClusterState(state)

	assert.True(t, view.Read)
	assert.Equal(t, processFactView{Registered: true, GoalAchieved: true}, view.ProcessStates["fresh-in-goal"])
	assert.Equal(t, processFactView{Registered: true, GoalAchieved: false}, view.ProcessStates["fresh-behind"])
	assert.Equal(t, processFactView{Registered: false, GoalAchieved: true}, view.ProcessStates["stale-in-goal"])
	assert.Equal(t, processFactView{Registered: true, GoalAchieved: false}, view.ProcessStates["never-achieved"])
}
