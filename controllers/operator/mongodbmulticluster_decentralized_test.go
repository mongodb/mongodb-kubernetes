package operator

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// This file is the M3 deliverable: a full deployment reaching Running in the unit-test world —
// the leader and one member reconciler per cluster over per-cluster fake clients, sharing one
// mocked Ops Manager connection (the CachedOMConnectionFactory caches by project name).

// decentralizedWorld is three clusters, each with its own fake API server holding a copy of the
// CR (the GitOps stand-in) plus the pre-provisioned project ConfigMap, credentials and agent
// key, one member reconciler each, and one leader on clusters[0].
type decentralizedWorld struct {
	m       *mdbmulti.MongoDBMultiCluster
	leader  *ReconcileMongoDBMultiClusterLeader
	members map[string]*ReconcileMongoDBDirective
	clients map[string]client.Client
	factory *om.CachedOMConnectionFactory
}

func newDecentralizedWorld(m *mdbmulti.MongoDBMultiCluster) *decentralizedWorld {
	w := &decentralizedWorld{
		m:       m,
		members: map[string]*ReconcileMongoDBDirective{},
		clients: map[string]client.Client{},
		factory: om.NewDefaultCachedOMConnectionFactory(),
	}
	// the static goal-state hook of the materializer tests cannot survive membership changes;
	// this one reports whatever the live mocked deployment currently holds
	withLiveDeploymentInGoalState(w.factory)
	for _, clusterName := range clusters {
		seeds := append([]client.Object{m.DeepCopy(), agentApiKeySecret(om.TestGroupID)}, mock.GetDefaultResources()...)
		w.members[clusterName], w.clients[clusterName] = materializerReconcilerForTest(NewStaticElector(clusterName, clusters[0]), w.factory, true, seeds...)
	}
	w.leader = newMongoDBMultiClusterLeaderReconciler(w.clients[clusters[0]], w.clients, NewStaticElector(clusters[0], clusters[0]), w.factory.GetConnectionFunc, nil, architectures.NonStatic)
	return w
}

// withLiveDeploymentInGoalState makes the mocked OM report every process currently in the
// deployment at goal version — removed processes drop out of the witness as the deployment
// mutates, which the scale-down ladder depends on.
func withLiveDeploymentInGoalState(omConnectionFactory *om.CachedOMConnectionFactory) {
	omConnectionFactory.SetPostCreateHook(func(conn om.Connection) {
		mockedConn := conn.(*om.MockedOmConnection)
		mockedConn.ReadAutomationStatusFunc = func() (*om.AutomationStatus, error) {
			automationStatus := &om.AutomationStatus{GoalVersion: 1}
			deployment, err := mockedConn.ReadDeployment()
			if err != nil {
				return automationStatus, nil
			}
			for i, process := range deployment.ProcessesCopy() {
				automationStatus.Processes = append(automationStatus.Processes, om.ProcessStatus{
					Hostname:                process.HostName(),
					Name:                    fmt.Sprintf("process-%d", i),
					LastGoalVersionAchieved: 1,
				})
			}
			return automationStatus, nil
		}
	})
}

// reconcileAll runs one pass of the leader and one pass of every member — the unit-test stand-in
// for the controllers' requeue loops.
func (w *decentralizedWorld) reconcileAll(ctx context.Context, t *testing.T) reconcile.Result {
	result, err := w.leader.Reconcile(ctx, requestFromObject(w.m))
	require.NoError(t, err)
	for _, clusterName := range clusters {
		_, err := w.members[clusterName].Reconcile(ctx, requestFromObject(w.m))
		require.NoError(t, err)
	}
	return result
}

// driveToRunning loops passes until the leader reports Running (its 24h requeue), returning the
// per-pass AC count history (consecutive duplicates collapsed).
func (w *decentralizedWorld) driveToRunning(ctx context.Context, t *testing.T, maxPasses int) []map[int]int {
	var history []map[int]int
	for i := 0; i < maxPasses; i++ {
		result := w.reconcileAll(ctx, t)
		if counts, ok := w.acCounts(); ok {
			if len(history) == 0 || !reflect.DeepEqual(history[len(history)-1], counts) {
				history = append(history, counts)
			}
		}
		if result.RequeueAfter == util.TWENTY_FOUR_HOURS {
			return history
		}
	}
	require.FailNow(t, "the deployment did not reach Running", "after %d passes; AC history: %v", maxPasses, history)
	return nil
}

func (w *decentralizedWorld) acCounts() (map[int]int, bool) {
	conn := w.factory.GetConnection()
	if conn == nil {
		return nil, false
	}
	deployment, err := conn.ReadDeployment()
	if err != nil {
		return nil, false
	}
	return acViewFromDeployment(deployment, w.m.Name).MemberCountsByIndex, true
}

// applySpecEverywhere is the GitOps stand-in: the same spec edit lands on every cluster's copy.
func (w *decentralizedWorld) applySpecEverywhere(ctx context.Context, t *testing.T, edit func(m *mdbmulti.MongoDBMultiCluster)) {
	edit(w.m)
	for _, clusterName := range clusters {
		copy := mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, w.m.Name), &copy))
		copy.Spec = *w.m.Spec.DeepCopy()
		require.NoError(t, w.clients[clusterName].Update(ctx, &copy))
	}
}

func (w *decentralizedWorld) readDirective(ctx context.Context, t *testing.T, clusterName string) operatorv1.MongoDBDirective {
	directive := operatorv1.MongoDBDirective{}
	require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, w.m.Name), &directive))
	return directive
}

func (w *decentralizedWorld) leaderPhase(ctx context.Context, t *testing.T) status.Phase {
	m := mdbmulti.MongoDBMultiCluster{}
	require.NoError(t, w.clients[clusters[0]].Get(ctx, kube.ObjectKey(w.m.Namespace, w.m.Name), &m))
	return m.Status.Phase
}

func TestDecentralizedFullDeployReachesRunning(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)

	w.driveToRunning(ctx, t, 30)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))

	wantHash := mustSpecHash(t, m.Spec)
	for _, item := range m.Spec.ClusterSpecList {
		directive := w.readDirective(ctx, t, item.ClusterName)
		assert.Equal(t, item.Members, directive.Spec.MemberCount)
		assert.True(t, directive.Status.StsApplied)
		assert.True(t, directive.Status.AgentRegistered)
		assert.True(t, directive.Status.InGoalState)
		assert.Equal(t, wantHash, directive.Status.ObservedSpecHash)
	}

	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	deployment, err := mockedConn.ReadDeployment()
	require.NoError(t, err)

	var wantProcessNames []string
	for i, item := range m.Spec.ClusterSpecList {
		for pod := 0; pod < item.Members; pod++ {
			wantProcessNames = append(wantProcessNames, fmt.Sprintf("%s-%d-%d", m.Name, i, pod))
		}
	}
	var gotProcessNames []string
	for _, process := range deployment.ProcessesCopy() {
		gotProcessNames = append(gotProcessNames, process.Name())
	}
	assert.ElementsMatch(t, wantProcessNames, gotProcessNames)

	term, ok := deployment.GetOperatorLeadershipTerm()
	assert.True(t, ok)
	assert.Equal(t, staticElectorTerm, term)

	// three operators, zero OM control-plane writes: the installer pre-provisions everything
	mockedConn.CheckOperationsDidntHappen(t,
		reflect.ValueOf(mockedConn.CreateProject),
		reflect.ValueOf(mockedConn.UpdateProject),
		reflect.ValueOf(mockedConn.GenerateAgentKey))
}

func TestDecentralizedScaleUpOneMemberAtATime(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)

	// two clusters must add one member each — the exclusivity rules make that four ordered steps
	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.ClusterSpecList[0].Members++
		m.Spec.ClusterSpecList[1].Members++
	})
	history := w.driveToRunning(ctx, t, 40)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	previous := baseline
	for _, counts := range history {
		changed := 0
		for idx, count := range counts {
			delta := count - previous[idx]
			if delta != 0 {
				changed++
				assert.Equal(t, 1, delta, "membership only ever grows one member per AC write: %v -> %v", previous, counts)
			}
		}
		assert.LessOrEqual(t, changed, 1, "never two clusters mid-change: %v -> %v", previous, counts)
		previous = counts
	}
	assert.Equal(t, map[int]int{0: m.Spec.ClusterSpecList[0].Members, 1: m.Spec.ClusterSpecList[1].Members, 2: m.Spec.ClusterSpecList[2].Members}, previous)
}

func TestDecentralizedScaleDownACFirst(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	originalMembers := m.Spec.ClusterSpecList[0].Members

	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.ClusterSpecList[0].Members--
	})

	// the inverted ladder: the AC must drop the member BEFORE the directive grant follows, and
	// the StatefulSet shrinks last
	acDroppedAt, directiveDroppedAt, stsDroppedAt := -1, -1, -1
	stsName := fmt.Sprintf("%s-0", m.Name)
	for pass := 0; pass < 40; pass++ {
		result := w.reconcileAll(ctx, t)
		if counts, ok := w.acCounts(); ok && acDroppedAt < 0 && counts[0] == originalMembers-1 {
			acDroppedAt = pass
		}
		if directiveDroppedAt < 0 && w.readDirective(ctx, t, clusters[0]).Spec.MemberCount == originalMembers-1 {
			directiveDroppedAt = pass
		}
		sts := appsv1.StatefulSet{}
		if err := w.clients[clusters[0]].Get(ctx, kube.ObjectKey(w.m.Namespace, stsName), &sts); err == nil {
			if stsDroppedAt < 0 && sts.Spec.Replicas != nil && int(*sts.Spec.Replicas) == originalMembers-1 {
				stsDroppedAt = pass
			}
		}
		if result.RequeueAfter == util.TWENTY_FOUR_HOURS {
			break
		}
	}

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	require.GreaterOrEqual(t, acDroppedAt, 0, "the AC never dropped the member")
	require.GreaterOrEqual(t, directiveDroppedAt, 0, "the directive never followed")
	require.GreaterOrEqual(t, stsDroppedAt, 0, "the StatefulSet never shrank")
	assert.LessOrEqual(t, acDroppedAt, directiveDroppedAt, "the AC write initiates the scale-down")
	assert.LessOrEqual(t, directiveDroppedAt, stsDroppedAt, "the member shrinks only on the advanced grant")
}

func TestDecentralizedScalingBothWaysRefused(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)
	baselineGrants := map[string]int{}
	for _, clusterName := range clusters {
		baselineGrants[clusterName] = w.readDirective(ctx, t, clusterName).Spec.MemberCount
	}

	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.ClusterSpecList[0].Members++
		m.Spec.ClusterSpecList[1].Members--
	})
	for i := 0; i < 3; i++ {
		w.reconcileAll(ctx, t)
	}

	assert.Equal(t, status.PhaseFailed, w.leaderPhase(ctx, t))
	counts, ok := w.acCounts()
	require.True(t, ok)
	assert.Equal(t, baseline, counts, "no AC movement on a refused spec")
	for _, clusterName := range clusters {
		assert.Equal(t, baselineGrants[clusterName], w.readDirective(ctx, t, clusterName).Spec.MemberCount, "no directive movement on a refused spec")
	}
}
