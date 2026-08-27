package operator

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// fakeElector returns a fixed leadership belief, for injecting arbitrary terms in tests; a
// non-nil floors slice records every term floor the leader pushes (T16).
type fakeElector struct {
	term     int64
	isLeader bool
	floors   *[]int64
}

func (e fakeElector) Current(types.NamespacedName) (term int64, isLeader bool) {
	return e.term, e.isLeader
}

func (e fakeElector) Events() <-chan event.GenericEvent { return nil }

func (e fakeElector) ObserveTermFloor(_ types.NamespacedName, floor int64) {
	if e.floors != nil {
		*e.floors = append(*e.floors, floor)
	}
}

// leaderReconcilerForTest builds a leader reconciler over one fake client per member cluster.
// The self cluster's client holds the CR plus the project ConfigMap and credentials Secret (the
// leader discovers the pre-provisioned OM project through them); the shared mocked factory is
// returned for OM-side assertions.
func leaderReconcilerForTest(m *mdbmulti.MongoDBMultiCluster, self string, elector Elector) (*ReconcileMongoDBMultiClusterLeader, map[string]client.Client, *om.CachedOMConnectionFactory) {
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	clientsMap := make(map[string]client.Client)
	for _, clusterName := range clusters {
		if clusterName == self {
			clientsMap[clusterName] = mock.NewEmptyFakeClientBuilder().WithObjects(m).WithObjects(mock.GetDefaultResources()...).Build()
		} else {
			clientsMap[clusterName] = mock.NewEmptyFakeClientBuilder().Build()
		}
	}
	reconciler := newMongoDBMultiClusterLeaderReconciler(clientsMap[self], newAPIServerTransport(clientsMap), elector, omConnectionFactory.GetConnectionFunc, nil, architectures.NonStatic, false)
	return reconciler, clientsMap, omConnectionFactory
}

// driveLeaderPasses reconciles the leader repeatedly; each pass executes at most one decision.
func driveLeaderPasses(ctx context.Context, t *testing.T, reconciler *ReconcileMongoDBMultiClusterLeader, m *mdbmulti.MongoDBMultiCluster, passes int) {
	for i := 0; i < passes; i++ {
		_, err := reconciler.Reconcile(ctx, requestFromObject(m))
		require.NoError(t, err, "pass %d", i)
	}
}

func TestLeaderFirstDeployWritesDirectivesOneDecisionPerPass(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap, omConnectionFactory := leaderReconcilerForTest(m, clusters[0], NewStaticElector(clusters[0], clusters[0]))

	// pass 1 records the allocation map on the first cluster at member count 0
	result, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Second}, result)

	directive := operatorv1.MongoDBDirective{}
	require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &directive))
	assert.Equal(t, 0, directive.Spec.MemberCount)
	assert.Equal(t, map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}, directive.Spec.IndexAllocations)
	err = clientsMap[clusters[2]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &operatorv1.MongoDBDirective{})
	assert.True(t, apiErrors.IsNotFound(err), "one decision per pass: the last cluster has no directive yet")

	// allocation push + three full-count advancements (first deploy runs parallel, no
	// convergence gate — the members never echo anything in this test)
	driveLeaderPasses(ctx, t, reconciler, m, 4)

	wantHash := mustSpecHash(t, m.Spec)
	for i, item := range m.Spec.ClusterSpecList {
		directive := operatorv1.MongoDBDirective{}
		require.NoError(t, clientsMap[item.ClusterName].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &directive))
		assert.Equal(t, item.ClusterName, directive.Spec.ClusterName)
		assert.Equal(t, staticElectorTerm, directive.Spec.LeadershipTerm)
		assert.Equal(t, wantHash, directive.Spec.TargetSpecHash)
		assert.Equal(t, item.Members, directive.Spec.MemberCount)
		assert.Equal(t, i, directive.Spec.ClusterIndex)
		assert.Equal(t, om.TestGroupID, directive.Spec.ProjectID, "the discovered project id is stamped")
		assert.False(t, directive.Spec.AdvancedAt.IsZero())
	}

	require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
	assert.Equal(t, status.PhasePending, m.Status.Phase)

	// with no member echoing agentRegistered, more passes must not publish the automation config
	driveLeaderPasses(ctx, t, reconciler, m, 2)
	mockedConn := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CheckNumberOfUpdateRequests(t, 0)
	mockedConn.CheckOperationsDidntHappen(t,
		reflect.ValueOf(mockedConn.CreateProject),
		reflect.ValueOf(mockedConn.UpdateProject),
		reflect.ValueOf(mockedConn.GenerateAgentKey))
}

func TestLeaderValidationFailureWritesNoDirective(t *testing.T) {
	ctx := context.Background()
	duplicateName := clusters[0]
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList([]string{duplicateName, duplicateName, clusters[2]}).Build()
	reconciler, clientsMap, _ := leaderReconcilerForTest(m, clusters[0], NewStaticElector(clusters[0], clusters[0]))

	result, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Second}, result)

	require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
	assert.Equal(t, status.PhaseFailed, m.Status.Phase)
	assert.Equal(t, fmt.Sprintf("Multiple clusters with the same name (%s) are not allowed", duplicateName), m.Status.Message)

	// the invalid spec never reached planning: no directive anywhere
	for clusterName, c := range clientsMap {
		err := c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &operatorv1.MongoDBDirective{})
		assert.True(t, apiErrors.IsNotFound(err), "no directive expected on cluster %s", clusterName)
	}
}

func TestNonLeaderWritesNothing(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap, _ := leaderReconcilerForTest(m, clusters[1], NewStaticElector(clusters[1], clusters[0]))

	result, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	for clusterName, c := range clientsMap {
		err := c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &operatorv1.MongoDBDirective{})
		assert.True(t, apiErrors.IsNotFound(err), "no directive expected on cluster %s", clusterName)
	}

	require.NoError(t, clientsMap[clusters[1]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
	assert.Equal(t, status.Phase(""), m.Status.Phase)
}

func TestElectorTermFlowsIntoDirectiveSpec(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap, _ := leaderReconcilerForTest(m, clusters[0], fakeElector{term: 42, isLeader: true})

	_, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)

	directive := operatorv1.MongoDBDirective{}
	require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &directive))
	assert.Equal(t, int64(42), directive.Spec.LeadershipTerm)
}

// TestLeaderPushesACTermFloorToElector pins the T16 push: the elector cannot read Ops Manager
// or peer directives, so after every snapshot the leader reports the highest term the world
// carries — AC-stamped term AND every visible directive's term — as the candidacy floor. The
// directive half is load-bearing: directives are rewritten on every takeover, the AC only when
// it changes, so after a total lease loss the directives usually carry the higher term (a live
// wiped ensemble healed to the AC's floor and then wedged forever below its directives).
func TestLeaderPushesACTermFloorToElector(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	var floors []int64
	reconciler, _, omConnectionFactory := leaderReconcilerForTest(m, clusters[0], fakeElector{term: 9, isLeader: true, floors: &floors})

	// the passes write directives at the elector's term 9; those terms feed the floor even
	// though the AC carries no stamp yet
	driveLeaderPasses(ctx, t, reconciler, m, 5)
	require.NotEmpty(t, floors)
	assert.Equal(t, int64(9), floors[len(floors)-1], "visible directive terms feed the floor before any AC stamp exists")

	// an AC stamped BELOW the directives must not lower the floor (max, never last-writer)
	conn := omConnectionFactory.GetConnection()
	payload := acPayload{LeadershipTerm: 7, MemberCounts: map[string]int{clusters[0]: 1, clusters[1]: 1, clusters[2]: 1}}
	require.NoError(t, reconciler.publishAutomationConfig(ctx, conn, m, payload, zap.S()))

	driveLeaderPasses(ctx, t, reconciler, m, 1)
	assert.Equal(t, int64(9), floors[len(floors)-1], "the floor is the max over AC and directives")

	// an AC stamped ABOVE every directive raises it
	payload.LeadershipTerm = 12
	require.NoError(t, reconciler.publishAutomationConfig(ctx, conn, m, payload, zap.S()))
	driveLeaderPasses(ctx, t, reconciler, m, 1)
	assert.Equal(t, int64(12), floors[len(floors)-1], "the AC-stamped term raises the floor past the directives")
}

func TestWriteDirectiveIsReadModifyWrite(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap, _ := leaderReconcilerForTest(m, clusters[0], NewStaticElector(clusters[0], clusters[0]))
	memberClient := clientsMap[clusters[1]]

	// a stored entry the planner does not carry (a ghost from a previous leader) must survive
	stored := buildDirective(m, clusters[1], 1, "old-hash")
	stored.Spec.IndexAllocations = map[string]int{clusters[1]: 1, "removed-cluster": 9}
	require.NoError(t, clientsMap[clusters[1]].Create(ctx, stored))

	desired := operatorv1.MongoDBDirectiveSpec{
		ClusterName:      clusters[1],
		LeadershipTerm:   2,
		TargetSpecHash:   "new-hash",
		MemberCount:      2,
		ClusterIndex:     1,
		IndexAllocations: map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2},
		ProjectID:        om.TestGroupID,
		AdvancedAt:       metav1.NewTime(time.Now().Truncate(time.Second)),
	}
	require.NoError(t, reconciler.transport.WriteDirective(ctx, clusters[1], kube.ObjectKey(m.Namespace, m.Name), desired))

	readBack := operatorv1.MongoDBDirective{}
	require.NoError(t, memberClient.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
	assert.Equal(t, 9, readBack.Spec.IndexAllocations["removed-cluster"], "union keeps the stored entry")
	assert.Equal(t, "new-hash", readBack.Spec.TargetSpecHash)
	assert.Equal(t, desired.AdvancedAt, readBack.Spec.AdvancedAt)

	// an unchanged instruction skips the write and keeps AdvancedAt, so stuckness stays visible
	resourceVersion := readBack.ResourceVersion
	repeat := desired
	repeat.AdvancedAt = metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))
	require.NoError(t, reconciler.transport.WriteDirective(ctx, clusters[1], kube.ObjectKey(m.Namespace, m.Name), repeat))
	require.NoError(t, memberClient.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
	assert.Equal(t, resourceVersion, readBack.ResourceVersion)
	assert.Equal(t, desired.AdvancedAt, readBack.Spec.AdvancedAt)
}

func TestPublishAutomationConfig(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	priority := func(p string) *string { return &p }
	m.Spec.ClusterSpecList[0].MemberConfig = []automationconfig.MemberOptions{{Priority: priority("2.0")}, {Priority: priority("1.5")}}
	m.Spec.ClusterSpecList[1].MemberConfig = []automationconfig.MemberOptions{{Priority: priority("3.0")}}
	reconciler, _, omConnectionFactory := leaderReconcilerForTest(m, clusters[0], NewStaticElector(clusters[0], clusters[0]))

	// directives (and their allocation maps) in place after the first-deploy passes
	driveLeaderPasses(ctx, t, reconciler, m, 5)
	conn := omConnectionFactory.GetConnection()

	payload := acPayload{LeadershipTerm: 7, MemberCounts: map[string]int{clusters[0]: 2, clusters[1]: 1, clusters[2]: 1}}
	require.NoError(t, reconciler.publishAutomationConfig(ctx, conn, m, payload, zap.S()))

	deployment, err := conn.ReadDeployment()
	require.NoError(t, err)
	term, ok := deployment.GetOperatorLeadershipTerm()
	assert.True(t, ok)
	assert.Equal(t, int64(7), term)

	rs := deployment.GetReplicaSetByName(m.Name)
	require.NotNil(t, rs)
	memberIds := rs.MemberIds()
	assert.Len(t, memberIds, 4)
	assert.Contains(t, memberIds, m.Name+"-0-0")
	assert.Contains(t, memberIds, m.Name+"-0-1")
	assert.Contains(t, memberIds, m.Name+"-1-0")
	assert.Contains(t, memberIds, m.Name+"-2-0")

	// member options follow the granted process order, not the spec's flattening
	prioritiesByName := map[string]float32{}
	for _, member := range rs.Members() {
		prioritiesByName[member.Name()] = member.Priority()
	}
	assert.Equal(t, float32(2.0), prioritiesByName[m.Name+"-0-0"])
	assert.Equal(t, float32(1.5), prioritiesByName[m.Name+"-0-1"])
	assert.Equal(t, float32(3.0), prioritiesByName[m.Name+"-1-0"])

	// republishing with one more member keeps the existing process ids stable
	payload.MemberCounts[clusters[1]] = 2
	require.NoError(t, reconciler.publishAutomationConfig(ctx, conn, m, payload, zap.S()))
	deployment, err = conn.ReadDeployment()
	require.NoError(t, err)
	assert.Equal(t, memberIds[m.Name+"-0-0"], deployment.GetReplicaSetByName(m.Name).MemberIds()[m.Name+"-0-0"])

	// log rotation is reconciled after the deployment write, like the legacy path
	mockedConn := conn.(*om.MockedOmConnection)
	mockedConn.CheckOrderOfOperations(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment), reflect.ValueOf(mockedConn.ReadUpdateAgentsLogRotation))
}

func TestExecuteMapsDecisionsToStatuses(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap, _ := leaderReconcilerForTest(m, clusters[0], NewStaticElector(clusters[0], clusters[0]))

	t.Run("InvalidSpec is terminal", func(t *testing.T) {
		_, err := reconciler.execute(ctx, m, nil, planDecision{Kind: decisionInvalidSpec, Reason: "scaling both ways"}, zap.S())
		require.NoError(t, err)
		require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
		assert.Equal(t, status.PhaseFailed, m.Status.Phase)
	})

	t.Run("NotProgressing is transient", func(t *testing.T) {
		_, err := reconciler.execute(ctx, m, nil, planDecision{Kind: decisionNotProgressing, Reason: "waiting"}, zap.S())
		require.NoError(t, err)
		require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
		assert.Equal(t, status.PhasePending, m.Status.Phase)
	})

	t.Run("Noop is Running", func(t *testing.T) {
		result, err := reconciler.execute(ctx, m, nil, planDecision{Kind: decisionNoop, Reason: "converged"}, zap.S())
		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > time.Hour)
		require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
		assert.Equal(t, status.PhaseRunning, m.Status.Phase)
	})
}
