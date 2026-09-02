package operator

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
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
	return newDecentralizedWorldWithLeaderElector(m, NewStaticElector(clusters[0], clusters[0]))
}

// newDecentralizedWorldWithLeaderElector injects the leader's elector — the term knob the T16
// and T17 scenarios turn; real-elector behavior is owned by the unit and envtest layers.
func newDecentralizedWorldWithLeaderElector(m *mdbmulti.MongoDBMultiCluster, leaderElector Elector) *decentralizedWorld {
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
	w.leader = newMongoDBMultiClusterLeaderReconciler(w.clients[clusters[0]], newAPIServerTransport(w.clients), leaderElector, w.factory.GetConnectionFunc, nil, architectures.NonStatic, false)
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

func (w *decentralizedWorld) deleteDirective(ctx context.Context, t *testing.T, clusterName string) {
	directive := w.readDirective(ctx, t, clusterName)
	require.NoError(t, w.clients[clusterName].Delete(ctx, &directive))
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

	crCopy := &mdbmulti.MongoDBMultiCluster{}
	require.NoError(t, w.clients[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), crCopy))
	var wantClusterStatuses []mdbmulti.ClusterStatusItem
	for _, item := range m.Spec.ClusterSpecList {
		wantClusterStatuses = append(wantClusterStatuses, mdbmulti.ClusterStatusItem{ClusterName: item.ClusterName, Members: item.Members})
	}
	assert.Equal(t, wantClusterStatuses, crCopy.Status.ClusterStatusList.ClusterStatuses)

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

// TestDecentralizedFreeParityPins pins the spec surface that reaches the automation config and
// the CR status through the reused legacy code, end to end in the three-operator world.
func TestDecentralizedFreeParityPins(t *testing.T) {
	ctx := context.Background()
	fcv := "7.0"
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	m.Spec.FeatureCompatibilityVersion = &fcv
	m.Spec.AdditionalMongodConfig = mdb.NewAdditionalMongodConfig("net.port", 30000)
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)

	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	deployment, err := mockedConn.ReadDeployment()
	require.NoError(t, err)
	processes := deployment.ProcessesCopy()
	require.NotEmpty(t, processes)
	for _, p := range processes {
		assert.Equal(t, fcv, p.FeatureCompatibilityVersion())
		assert.NotNil(t, maputil.ReadMapValueAsInterface(p.Args(), "net", "port"), "the custom port reaches the AC process args")
	}

	crCopy := &mdbmulti.MongoDBMultiCluster{}
	require.NoError(t, w.clients[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), crCopy))
	assert.Equal(t, fcv, crCopy.Status.FeatureCompatibilityVersion, "set by UpdateStatus on PhaseRunning")
}

func TestDecentralizedMongodConfigRemovalUnmerges(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	m.Spec.AdditionalMongodConfig = mdb.NewAdditionalMongodConfig("setParameter.maxIndexBuildMemoryUsageMegabytes", 150)
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)

	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	deployment, err := mockedConn.ReadDeployment()
	require.NoError(t, err)
	processes := deployment.ProcessesCopy()
	require.NotEmpty(t, processes)
	for _, p := range processes {
		assert.NotNil(t, maputil.ReadMapValueAsInterface(p.Args(), "setParameter", "maxIndexBuildMemoryUsageMegabytes"))
	}

	// removing the option must un-merge it — a content-only change, no member moves
	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.AdditionalMongodConfig = nil
	})
	w.driveToRunning(ctx, t, 30)

	deployment, err = mockedConn.ReadDeployment()
	require.NoError(t, err)
	for _, p := range deployment.ProcessesCopy() {
		assert.Nil(t, maputil.ReadMapValueAsInterface(p.Args(), "setParameter", "maxIndexBuildMemoryUsageMegabytes"))
	}

	// the diff base lives on the leader's OWN cluster copy only
	for i, clusterName := range clusters {
		crCopy := &mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), crCopy))
		if i == 0 {
			assert.Contains(t, crCopy.Annotations, util.LastAchievedSpec)
		} else {
			assert.NotContains(t, crCopy.Annotations, util.LastAchievedSpec)
		}
	}
}

func TestDecentralizedUnsupportedSpecRefused(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	baselineGrants := map[string]int{}
	for _, clusterName := range clusters {
		baselineGrants[clusterName] = w.readDirective(ctx, t, clusterName).Spec.MemberCount
	}

	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.Security = &mdb.Security{Authentication: &mdb.Authentication{Enabled: true}}
		m.Spec.ClusterSpecList[0].Members++
	})
	for i := 0; i < 3; i++ {
		w.reconcileAll(ctx, t)
	}

	assert.Equal(t, status.PhaseFailed, w.leaderPhase(ctx, t))
	for _, clusterName := range clusters {
		assert.Equal(t, baselineGrants[clusterName], w.readDirective(ctx, t, clusterName).Spec.MemberCount, "no directive advancement on a refused spec")
	}
}

// --- M8 static TLS: pre-provisioned per-cluster materials, consumed locally ---

// seedTLSMaterialsEverywhere is the installer/fixture contract in the unit world: ONE cert is
// minted and the same bytes land on every cluster (byte-identical by construction), alongside the
// CA ConfigMap. Returns the source secret name.
func seedTLSMaterialsEverywhere(ctx context.Context, t *testing.T, w *decentralizedWorld, caName string) string {
	crt, key := createMockCertAndKeyBytes()
	secretName := fmt.Sprintf("%s-%s-cert", w.m.Spec.Security.CertificatesSecretsPrefix, w.m.Name)
	for _, clusterName := range clusters {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: caName, Namespace: w.m.Namespace},
			Data:       map[string]string{"ca-pem": "capublickey", "mms-ca.crt": "capublickey"},
		}
		require.NoError(t, w.clients[clusterName].Create(ctx, cm))
		secret := &corev1.Secret{
			Type:       corev1.SecretTypeTLS,
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: w.m.Namespace},
			Data:       map[string][]byte{"tls.crt": crt, "tls.key": key},
		}
		require.NoError(t, w.clients[clusterName].Create(ctx, secret))
	}
	return secretName
}

func tlsMultiReplicaSet() *mdbmulti.MongoDBMultiCluster {
	return mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).SetSecurity(&mdb.Security{
		TLSConfig:                 &mdb.TLSConfig{Enabled: true, CA: "issuer-ca"},
		CertificatesSecretsPrefix: "clustercert",
	}).Build()
}

// reconcileAllTolerant is reconcileAll for worlds expected to hold: errors are collected instead
// of failing the test — the M8 contract makes missing materials a loud backoff error.
func (w *decentralizedWorld) reconcileAllTolerant(ctx context.Context) []error {
	var errs []error
	if _, err := w.leader.Reconcile(ctx, requestFromObject(w.m)); err != nil {
		errs = append(errs, err)
	}
	for _, clusterName := range clusters {
		if _, err := w.members[clusterName].Reconcile(ctx, requestFromObject(w.m)); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// TestDecentralizedTLSDeployReachesRunning is the M8 static-TLS proof in the unit world: with
// the source cert secret and CA ConfigMap pre-provisioned byte-identically on every cluster, a
// TLS-enabled spec deploys to Running, every member collates its own -pem secret locally, and
// the AC's certificateKeyFile embeds the hash the leader computed from its own local copy.
func TestDecentralizedTLSDeployReachesRunning(t *testing.T) {
	ctx := context.Background()
	w := newDecentralizedWorld(tlsMultiReplicaSet())
	sourceSecretName := seedTLSMaterialsEverywhere(ctx, t, w, "issuer-ca")

	w.driveToRunning(ctx, t, 30)
	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))

	// the -pem secret's latestHash bookkeeping key IS the shared hash (all copies byte-identical)
	pemSecret := corev1.Secret{}
	require.NoError(t, w.clients[clusters[0]].Get(ctx, kube.ObjectKey(w.m.Namespace, sourceSecretName+"-pem"), &pemSecret))
	wantHash := string(pemSecret.Data["latestHash"])
	require.NotEmpty(t, wantHash)

	processes := w.factory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	require.NotEmpty(t, processes)
	for _, p := range processes {
		assert.True(t, p.IsTLSEnabled())
		assert.Equal(t, "requireTLS", p.TLSConfig()["mode"])
		assert.Equal(t, fmt.Sprintf("%s/%s", util.TLSCertMountPath, wantHash), p.TLSConfig()["certificateKeyFile"])
	}

	for _, clusterName := range clusters {
		memberPem := corev1.Secret{}
		require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, sourceSecretName+"-pem"), &memberPem))
		assert.Contains(t, memberPem.Data, wantHash, "each member collates its own PEM keyed by the shared hash")
	}
}

// TestDecentralizedTLSHoldsUntilMaterialsArrive pins the contract-unmet behavior: a TLS spec
// with no pre-provisioned materials holds loudly (errors naming the missing material, never a
// StatefulSet), and converges to Running once the materials land — no operator restart needed.
func TestDecentralizedTLSHoldsUntilMaterialsArrive(t *testing.T) {
	ctx := context.Background()
	w := newDecentralizedWorld(tlsMultiReplicaSet())

	sawMissingMaterial := false
	for i := 0; i < 6; i++ {
		for _, err := range w.reconcileAllTolerant(ctx) {
			if strings.Contains(err.Error(), "not pre-provisioned") {
				sawMissingMaterial = true
			}
		}
	}
	assert.True(t, sawMissingMaterial, "the hold must name the missing material")
	assert.NotEqual(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	for _, clusterName := range clusters {
		sts := appsv1.StatefulSet{}
		err := w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, fmt.Sprintf("%s-0", w.m.Name)), &sts)
		assert.True(t, apiErrors.IsNotFound(err), "no StatefulSet before the TLS materials exist on %s", clusterName)
	}

	seedTLSMaterialsEverywhere(ctx, t, w, "issuer-ca")
	w.driveToRunning(ctx, t, 40)
	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
}

// TestDecentralizedOMCAConfigMapMissingHoldsBeforePods pins the OM-CA wedge closure (poc-audit
// §2's "unguarded twin"): the database pods mount the ConfigMap named by the project's
// sslMMSCAConfigMap, so a member whose cluster lacks it must hold loudly BEFORE any StatefulSet
// exists — never wedged-in-ContainerCreating pods. The hold comes from ReadProjectConfig reading
// the ConfigMap contents through the local client; this test pins that path staying local and
// loud.
func TestDecentralizedOMCAConfigMapMissingHoldsBeforePods(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)

	projectCM := corev1.ConfigMap{}
	require.NoError(t, w.clients[clusters[1]].Get(ctx, kube.ObjectKey(m.Namespace, mock.TestProjectConfigMapName), &projectCM))
	projectCM.Data[util.SSLMMSCAConfigMap] = "om-ca"
	require.NoError(t, w.clients[clusters[1]].Update(ctx, &projectCM))

	sawMissingOMCA := false
	for i := 0; i < 6; i++ {
		for _, err := range w.reconcileAllTolerant(ctx) {
			if strings.Contains(err.Error(), "om-ca") {
				sawMissingOMCA = true
			}
		}
	}
	assert.True(t, sawMissingOMCA, "the hold must name the missing OM CA ConfigMap")
	sts := appsv1.StatefulSet{}
	err := w.clients[clusters[1]].Get(ctx, kube.ObjectKey(m.Namespace, fmt.Sprintf("%s-1", m.Name)), &sts)
	assert.True(t, apiErrors.IsNotFound(err), "no StatefulSet on the cluster missing the OM CA ConfigMap")
}

// TestDecentralizedTLSLeaderRefusesEmptyHash pins the silent-fallback defusal:
// pem.ReadHashFromSecret quietly returns "" when the source secret is missing (or Opaque), and
// legacy would publish certificateKeyFile: "" — wrong-but-running. The leader must refuse the
// publish outright, before any AC write.
func TestDecentralizedTLSLeaderRefusesEmptyHash(t *testing.T) {
	ctx := context.Background()
	w := newDecentralizedWorld(tlsMultiReplicaSet())
	conn := om.NewMockedOmConnection(om.NewDeployment())

	err := w.leader.publishAutomationConfig(ctx, conn, w.m, acPayload{MemberCounts: map[string]int{clusters[0]: 1}, LeadershipTerm: 1}, zap.S())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PEM hash")
	conn.CheckOperationsDidntHappen(t, reflect.ValueOf(conn.ReadUpdateDeployment))
}

// TestDecentralizedDirectiveDeletionIsASafeReset pins the seed rule end to end (backlog T2):
// a directive deleted in steady state is recreated at the AC count — recognition of existing
// capacity — and the world returns to Noop with zero AC writes and zero StatefulSet movement.
func TestDecentralizedDirectiveDeletionIsASafeReset(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)
	baselineGrant := w.readDirective(ctx, t, clusters[1]).Spec.MemberCount
	stsName := fmt.Sprintf("%s-1", m.Name)
	stsBefore := appsv1.StatefulSet{}
	require.NoError(t, w.clients[clusters[1]].Get(ctx, kube.ObjectKey(m.Namespace, stsName), &stsBefore))
	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CleanHistory()

	w.deleteDirective(ctx, t, clusters[1])
	history := w.driveToRunning(ctx, t, 30)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	assert.Equal(t, baselineGrant, w.readDirective(ctx, t, clusters[1]).Spec.MemberCount, "recreated at the AC count, never 0")
	assert.Equal(t, []map[int]int{baseline}, history, "the AC membership never moved")
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))
	stsAfter := appsv1.StatefulSet{}
	require.NoError(t, w.clients[clusters[1]].Get(ctx, kube.ObjectKey(m.Namespace, stsName), &stsAfter))
	assert.Equal(t, *stsBefore.Spec.Replicas, *stsAfter.Spec.Replicas, "the StatefulSet never scaled")
}

// TestDecentralizedDirectiveLossDuringScaleUpNeverDropsPeers pins backlog T1 end to end: a
// directive deleted while ANOTHER cluster is mid-scale-up never costs the deleted cluster's
// live members — every AC write keeps carrying them, and the interrupted ladder completes.
func TestDecentralizedDirectiveLossDuringScaleUpNeverDropsPeers(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)

	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.ClusterSpecList[0].Members++
	})
	w.reconcileAll(ctx, t) // cluster 0 is granted +1 and mid-ladder when the deletion hits
	w.deleteDirective(ctx, t, clusters[1])
	history := w.driveToRunning(ctx, t, 40)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	for _, counts := range history {
		assert.Equal(t, baseline[1], counts[1], "cluster 1's live members never leave the AC: %v", history)
	}
	finalCounts, ok := w.acCounts()
	require.True(t, ok)
	assert.Equal(t, map[int]int{0: baseline[0] + 1, 1: baseline[1], 2: baseline[2]}, finalCounts)
}

// TestDecentralizedAllocationMapRecoversFromOneSurvivor pins backlog T10: with two directives
// lost, the full allocation map recovers from the single surviving copy — grants reseed at the
// AC counts, indexes and StatefulSet names never change, and the AC is never touched.
func TestDecentralizedAllocationMapRecoversFromOneSurvivor(t *testing.T) {
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
	fullAllocations := map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}
	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CleanHistory()

	w.deleteDirective(ctx, t, clusters[1])
	w.deleteDirective(ctx, t, clusters[2])
	history := w.driveToRunning(ctx, t, 40)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	for i, clusterName := range clusters {
		directive := w.readDirective(ctx, t, clusterName)
		assert.Equal(t, i, directive.Spec.ClusterIndex, "indexes never change on recovery")
		assert.Equal(t, fullAllocations, directive.Spec.IndexAllocations, "the map rides every copy")
		assert.Equal(t, baselineGrants[clusterName], directive.Spec.MemberCount)
	}
	assert.Equal(t, []map[int]int{baseline}, history, "the AC membership never moved")
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))
	for i := 1; i < 3; i++ {
		sts := appsv1.StatefulSet{}
		require.NoError(t, w.clients[clusters[i]].Get(ctx, kube.ObjectKey(m.Namespace, fmt.Sprintf("%s-%d", m.Name, i)), &sts), "no renamed StatefulSets")
	}
}

// TestDecentralizedTotalStateLossFreezesThenRunbookHeals pins backlog T8 + T9 end to end: with
// every directive lost over a live AC the leader freezes naming the majority-loss runbook and
// writes NOTHING; the runbook then writes ONE directive carrying the recovered map — its term
// and spec hash are readable from the AC's stamped markers, the member count from the AC
// itself — and the world self-heals without a single AC write.
func TestDecentralizedTotalStateLossFreezesThenRunbookHeals(t *testing.T) {
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
	projectID := w.readDirective(ctx, t, clusters[0]).Spec.ProjectID
	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CleanHistory()

	for _, clusterName := range clusters {
		w.deleteDirective(ctx, t, clusterName)
	}
	for i := 0; i < 3; i++ {
		w.reconcileAll(ctx, t)
	}

	// frozen: Pending naming the runbook, zero writes of any kind
	assert.Equal(t, status.PhasePending, w.leaderPhase(ctx, t))
	crCopy := &mdbmulti.MongoDBMultiCluster{}
	require.NoError(t, w.clients[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), crCopy))
	assert.Contains(t, crCopy.Status.Message, "majority-loss runbook")
	for _, clusterName := range clusters {
		err := w.clients[clusterName].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &operatorv1.MongoDBDirective{})
		assert.True(t, apiErrors.IsNotFound(err), "no directive may be guessed into existence on cluster %s", clusterName)
	}
	frozenCounts, ok := w.acCounts()
	require.True(t, ok)
	assert.Equal(t, baseline, frozenCounts)
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))

	// the runbook: every field is recoverable from surviving state
	deployment, err := mockedConn.ReadDeployment()
	require.NoError(t, err)
	term, ok := deployment.GetOperatorLeadershipTerm()
	require.True(t, ok)
	specHash, ok := deployment.GetOperatorSpecHash()
	require.True(t, ok)
	runbookDirective := &operatorv1.MongoDBDirective{
		ObjectMeta: metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace},
		Spec: operatorv1.MongoDBDirectiveSpec{
			ClusterName:      clusters[0],
			LeadershipTerm:   term,
			TargetSpecHash:   specHash,
			MemberCount:      baseline[0],
			ClusterIndex:     0,
			IndexAllocations: map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2},
			ProjectID:        projectID,
		},
	}
	require.NoError(t, w.clients[clusters[0]].Create(ctx, runbookDirective))
	history := w.driveToRunning(ctx, t, 40)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	for _, clusterName := range clusters {
		assert.Equal(t, baselineGrants[clusterName], w.readDirective(ctx, t, clusterName).Spec.MemberCount)
	}
	assert.Equal(t, []map[int]int{baseline}, history, "the AC membership never moved")
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))
}

// stsReplicas reads every cluster's StatefulSet replica count, keyed by cluster name.
func (w *decentralizedWorld) stsReplicas(ctx context.Context, t *testing.T) map[string]int32 {
	replicas := map[string]int32{}
	for i, clusterName := range clusters {
		sts := appsv1.StatefulSet{}
		require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, fmt.Sprintf("%s-%d", w.m.Name, i)), &sts))
		require.NotNil(t, sts.Spec.Replicas)
		replicas[clusterName] = *sts.Spec.Replicas
	}
	return replicas
}

func (w *decentralizedWorld) directiveTerms(ctx context.Context, t *testing.T) map[string]int64 {
	terms := map[string]int64{}
	for _, clusterName := range clusters {
		terms[clusterName] = w.readDirective(ctx, t, clusterName).Spec.LeadershipTerm
	}
	return terms
}

// TestDecentralizedStaleLeaderWedgesThenTermBumpHeals pins T16 end to end: a leader whose term
// fell below the AC-stamped term (every lease lost, then a naive re-election) wedges on
// staleWorld writing nothing; the wedged pass itself pushes the AC term as the floor, and once
// the elector re-elects above it the directives are rewritten at the new term one at a time,
// the members re-echo, and the world returns to Running with zero AC writes and zero pod
// movement.
func TestDecentralizedStaleLeaderWedgesThenTermBumpHeals(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	var floors []int64
	elector := &fakeElector{term: 5, isLeader: true, floors: &floors}
	w := newDecentralizedWorldWithLeaderElector(m, elector)
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)
	replicasBefore := w.stsReplicas(ctx, t)
	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CleanHistory()

	// every lease lost: the re-elected term never saw the AC's stamped 5
	elector.term = 3
	for i := 0; i < 3; i++ {
		w.reconcileAll(ctx, t)
	}

	assert.Equal(t, status.PhasePending, w.leaderPhase(ctx, t))
	crCopy := &mdbmulti.MongoDBMultiCluster{}
	require.NoError(t, w.clients[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), crCopy))
	assert.Contains(t, crCopy.Status.Message, "newer leadership term")
	assert.Equal(t, map[string]int64{clusters[0]: 5, clusters[1]: 5, clusters[2]: 5}, w.directiveTerms(ctx, t), "a wedged leader writes nothing")
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))
	require.NotEmpty(t, floors)
	assert.Equal(t, int64(5), floors[len(floors)-1], "the wedged pass still pushes the AC term as the floor")

	// the elector honors the floor: the next candidacy lands at floor+1
	elector.term = floors[len(floors)-1] + 1
	previous := w.directiveTerms(ctx, t)
	for pass := 0; pass < 30; pass++ {
		result := w.reconcileAll(ctx, t)
		current := w.directiveTerms(ctx, t)
		changed := 0
		for clusterName, term := range current {
			if term != previous[clusterName] {
				changed++
				assert.Equal(t, int64(6), term, "rewrites carry the bumped term")
			}
		}
		assert.LessOrEqual(t, changed, 1, "directives are rewritten one at a time: %v -> %v", previous, current)
		previous = current
		if result.RequeueAfter == util.TWENTY_FOUR_HOURS {
			break
		}
	}

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	assert.Equal(t, map[string]int64{clusters[0]: 6, clusters[1]: 6, clusters[2]: 6}, w.directiveTerms(ctx, t))
	counts, ok := w.acCounts()
	require.True(t, ok)
	assert.Equal(t, baseline, counts, "the AC membership never moved")
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))
	assert.Equal(t, replicasBefore, w.stsReplicas(ctx, t), "zero pod movement")
}

// TestDecentralizedZombieLeaderSingleStaleWriteSelfHeals pins T17 end to end: a deposed leader
// gets one last directive write in at its old term; the live leader recognizes the downgrade and
// rewrites it once at the current term — one write of churn, no oscillation, no AC or pod
// movement.
func TestDecentralizedZombieLeaderSingleStaleWriteSelfHeals(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	w := newDecentralizedWorldWithLeaderElector(m, &fakeElector{term: 5, isLeader: true})
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)
	baselineGrant := w.readDirective(ctx, t, clusters[1]).Spec.MemberCount
	replicasBefore := w.stsReplicas(ctx, t)
	mockedConn := w.factory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CleanHistory()

	// the zombie's write: same instruction, old term — its belief was stale, not its plan
	zombieWrite := w.readDirective(ctx, t, clusters[1])
	zombieWrite.Spec.LeadershipTerm = 4
	require.NoError(t, w.clients[clusters[1]].Update(ctx, &zombieWrite))

	rewrites := 0
	previousTerm := int64(4)
	for pass := 0; pass < 30; pass++ {
		result := w.reconcileAll(ctx, t)
		term := w.readDirective(ctx, t, clusters[1]).Spec.LeadershipTerm
		if term != previousTerm {
			rewrites++
			previousTerm = term
		}
		if result.RequeueAfter == util.TWENTY_FOUR_HOURS {
			break
		}
	}

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))
	assert.Equal(t, 1, rewrites, "exactly one healing write, no oscillation")
	assert.Equal(t, int64(5), previousTerm, "the directive is back at the live term")
	assert.Equal(t, baselineGrant, w.readDirective(ctx, t, clusters[1]).Spec.MemberCount)
	counts, ok := w.acCounts()
	require.True(t, ok)
	assert.Equal(t, baseline, counts, "the AC membership never moved")
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment))
	assert.Equal(t, replicasBefore, w.stsReplicas(ctx, t), "zero pod movement")
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

// TestDecentralizedAddClusterToLiveDeploymentReachesRunning pins M7 end to end: a cluster added
// to the spec of a live two-cluster deployment gets its first directive and grows to Running one
// member at a time, without disturbing either original cluster. Before the fix this wedged
// forever — advancement's fence ("never advance a member past what it has seen") required
// memberCaughtUp, which itself required the directive to exist, refusing the one cluster that had
// never had a directive to see anything (found live: an EVG scale_up_cluster run stuck on
// "waiting for cluster ...: no directive written yet").
func TestDecentralizedAddClusterToLiveDeploymentReachesRunning(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters[:2]).Build()
	w := newDecentralizedWorld(m)
	w.driveToRunning(ctx, t, 30)
	baseline, ok := w.acCounts()
	require.True(t, ok)

	replicasBefore := map[string]int32{}
	for i, clusterName := range clusters[:2] {
		sts := appsv1.StatefulSet{}
		require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, fmt.Sprintf("%s-%d", w.m.Name, i)), &sts))
		require.NotNil(t, sts.Spec.Replicas)
		replicasBefore[clusterName] = *sts.Spec.Replicas
	}

	w.applySpecEverywhere(ctx, t, func(m *mdbmulti.MongoDBMultiCluster) {
		m.Spec.ClusterSpecList = append(m.Spec.ClusterSpecList, mdb.ClusterSpecItem{ClusterName: clusters[2], Members: 2})
	})
	history := w.driveToRunning(ctx, t, 40)

	assert.Equal(t, status.PhaseRunning, w.leaderPhase(ctx, t))

	// one-at-a-time: the new cluster's index climbs 0 -> 1 -> 2 (its allocated index is the
	// third), nothing else's count ever moves in the same AC write
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
	assert.Equal(t, map[int]int{0: baseline[0], 1: baseline[1], 2: 2}, previous)

	fullAllocations := map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}
	for _, clusterName := range clusters {
		directive := w.readDirective(ctx, t, clusterName)
		assert.Equal(t, fullAllocations, directive.Spec.IndexAllocations, "the extended map rides every copy")
	}
	assert.Equal(t, 2, w.readDirective(ctx, t, clusters[2]).Spec.MemberCount)

	for i, clusterName := range clusters[:2] {
		sts := appsv1.StatefulSet{}
		require.NoError(t, w.clients[clusterName].Get(ctx, kube.ObjectKey(w.m.Namespace, fmt.Sprintf("%s-%d", w.m.Name, i)), &sts))
		assert.Equal(t, replicasBefore[clusterName], *sts.Spec.Replicas, "the addition moves only the new cluster's StatefulSet")
	}
}
