package operator

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/handler"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// materializerReconcilerForTest builds a member reconciler over one fake client with the
// standard test seams: the Get interceptor that marks StatefulSets ready and (optionally)
// registers their hostnames on the mocked OM connection.
func materializerReconcilerForTest(elector Elector, omConnectionFactory *om.CachedOMConnectionFactory, addOMHosts bool, objects ...client.Object) (*ReconcileMongoDBDirective, client.Client) {
	mock.InitDefaultEnvVariables()
	c := mock.NewEmptyFakeClientBuilder().
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, addOMHosts)}).
		Build()
	return newMongoDBDirectiveReconciler(c, elector, nil, "", "", architectures.NonStatic, omConnectionFactory.GetConnectionFunc), c
}

// materializerSeeds returns the standard cluster contents for a materializer test: the CR, its
// matching directive for clusters[0], the project ConfigMap + credentials Secret, and the
// pre-provisioned agent API key Secret.
func materializerSeeds(t *testing.T, m *mdbmulti.MongoDBMultiCluster) (*operatorv1.MongoDBDirective, []client.Object) {
	directive := buildDirective(m, clusters[0], staticElectorTerm, mustSpecHash(t, m.Spec))
	directive.Spec.ProjectID = om.TestGroupID
	directive.Spec.IndexAllocations = map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}
	objects := append([]client.Object{m, directive, agentApiKeySecret(om.TestGroupID)}, mock.GetDefaultResources()...)
	return directive, objects
}

func agentApiKeySecret(projectID string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: agents.ApiKeySecretName(projectID), Namespace: mock.TestNamespace},
		Data:       map[string][]byte{"agentApiKey": []byte("qwerty")},
	}
}

// withGoalStateReached makes the mocked OM report every given hostname at goal version. The
// default mock builds the automation status without hostnames, so real hostname-keyed goal
// lookups can never succeed against it (registered-but-never-in-goal) — this is the test seam.
func withGoalStateReached(omConnectionFactory *om.CachedOMConnectionFactory, hostnames []string) {
	omConnectionFactory.SetPostCreateHook(func(conn om.Connection) {
		conn.(*om.MockedOmConnection).ReadAutomationStatusFunc = func() (*om.AutomationStatus, error) {
			status := &om.AutomationStatus{GoalVersion: 1}
			for i, hostname := range hostnames {
				status.Processes = append(status.Processes, om.ProcessStatus{
					Hostname:                hostname,
					Name:                    fmt.Sprintf("process-%d", i),
					LastGoalVersionAchieved: 1,
				})
			}
			return status, nil
		}
	})
}

func ownProcessHostnames(m *mdbmulti.MongoDBMultiCluster, clusterIndex, members int) []string {
	return dns.GetMultiClusterProcessHostnames(m.Name, m.Namespace, clusterIndex, members, "", nil)
}

func TestMaterializerAdvancePath(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	directive, objects := materializerSeeds(t, m)
	grantedMembers := directive.Spec.MemberCount

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	withGoalStateReached(omConnectionFactory, ownProcessHostnames(m, 0, grantedMembers))
	reconciler, c := materializerReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), omConnectionFactory, true, objects...)

	result, err := reconciler.Reconcile(ctx, requestFromObject(directive))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{RequeueAfter: directiveHoldRetry}, result)

	// the StatefulSet slice: granted count, per-cluster name, the enqueue annotation
	sts := appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, fmt.Sprintf("%s-0", m.Name)), &sts))
	assert.Equal(t, int32(grantedMembers), *sts.Spec.Replicas)
	assert.Equal(t, m.Name, sts.Annotations[handler.MongoDBMultiResourceAnnotation])

	// services: SRV, headless, one per own pod
	for _, svcName := range append([]string{
		fmt.Sprintf("%s-svc", m.Name),
		fmt.Sprintf("%s-0-svc", m.Name),
	}, podServiceNames(m.Name, 0, grantedMembers)...) {
		svc := corev1.Service{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, svcName), &svc), "expected service %s", svcName)
	}

	// hostname-override ConfigMap covers exactly the granted pods
	cm := corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, fmt.Sprintf("%s-hostname-override", m.Name)), &cm))
	assert.Len(t, cm.Data, grantedMembers)

	readBack := operatorv1.MongoDBDirective{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
	assert.True(t, readBack.Status.StsApplied)

	// the ready-marking interceptor fires on a successful StatefulSet Get, which the create
	// pass never performs — the agents register on the second pass's update path, mirroring
	// how the facts converge over passes in the real world
	_, err = reconciler.Reconcile(ctx, requestFromObject(directive))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
	assert.True(t, readBack.Status.StsApplied)
	assert.True(t, readBack.Status.AgentRegistered)
	assert.True(t, readBack.Status.InGoalState)
}

func podServiceNames(name string, clusterIndex, members int) []string {
	names := make([]string, members)
	for podNum := range members {
		names[podNum] = dns.GetMultiServiceName(name, clusterIndex, podNum)
	}
	return names
}

func readDirectiveStatus(ctx context.Context, t *testing.T, c client.Client, m *mdbmulti.MongoDBMultiCluster) operatorv1.MongoDBDirectiveStatus {
	readBack := operatorv1.MongoDBDirective{}
	require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
	return readBack.Status
}

func TestMaterializerFactsStayFalse(t *testing.T) {
	ctx := context.Background()

	t.Run("agents never registered", func(t *testing.T) {
		m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		directive, objects := materializerSeeds(t, m)
		omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
		reconciler, c := materializerReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), omConnectionFactory, false, objects...)

		for range 2 {
			_, err := reconciler.Reconcile(ctx, requestFromObject(directive))
			require.NoError(t, err)
		}

		status := readDirectiveStatus(ctx, t, c, m)
		assert.True(t, status.StsApplied)
		assert.False(t, status.AgentRegistered)
		assert.False(t, status.InGoalState)
	})

	t.Run("stale agent ping is not registered", func(t *testing.T) {
		m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		directive, objects := materializerSeeds(t, m)
		omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
		omConnectionFactory.SetPostCreateHook(func(conn om.Connection) {
			conn.(*om.MockedOmConnection).ReadAutomationAgentsFunc = func(int) (om.Paginated, error) {
				agentStatuses := make([]om.AgentStatus, 0)
				for _, hostname := range ownProcessHostnames(m, 0, directive.Spec.MemberCount) {
					agentStatuses = append(agentStatuses, om.AgentStatus{
						Hostname: hostname,
						TypeName: "AUTOMATION",
						LastConf: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
					})
				}
				return om.AutomationAgentStatusResponse{AutomationAgents: agentStatuses}, nil
			}
		})
		reconciler, c := materializerReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), omConnectionFactory, true, objects...)

		for range 2 {
			_, err := reconciler.Reconcile(ctx, requestFromObject(directive))
			require.NoError(t, err)
		}

		status := readDirectiveStatus(ctx, t, c, m)
		assert.True(t, status.StsApplied)
		assert.False(t, status.AgentRegistered)
	})

	t.Run("registered but not in goal state", func(t *testing.T) {
		// no goal-state hook: the default mock builds the automation status without hostnames,
		// so hostname-keyed goal lookups stay unachieved while registration succeeds
		m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		directive, objects := materializerSeeds(t, m)
		omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
		reconciler, c := materializerReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), omConnectionFactory, true, objects...)

		for range 2 {
			_, err := reconciler.Reconcile(ctx, requestFromObject(directive))
			require.NoError(t, err)
		}

		status := readDirectiveStatus(ctx, t, c, m)
		assert.True(t, status.StsApplied)
		assert.True(t, status.AgentRegistered)
		assert.False(t, status.InGoalState)
	})
}

func TestMaterializerNeverWritesOM(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	directive, objects := materializerSeeds(t, m)
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, _ := materializerReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), omConnectionFactory, true, objects...)

	for range 2 {
		_, err := reconciler.Reconcile(ctx, requestFromObject(directive))
		require.NoError(t, err)
	}

	conn := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	conn.CheckOperationsDidntHappen(t,
		reflect.ValueOf(conn.UpdateDeployment),
		reflect.ValueOf(conn.ReadUpdateDeployment),
		reflect.ValueOf(conn.CreateProject),
		reflect.ValueOf(conn.UpdateProject),
		reflect.ValueOf(conn.GenerateAgentKey))
}

func TestMaterializerRequiresPreProvisionedAgentKey(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	directive, objects := materializerSeeds(t, m)
	// drop the pre-provisioned agent API key secret from the seeds
	seeds := make([]client.Object, 0, len(objects)-1)
	for _, o := range objects {
		if o.GetName() == agents.ApiKeySecretName(om.TestGroupID) {
			continue
		}
		seeds = append(seeds, o)
	}
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, c := materializerReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), omConnectionFactory, true, seeds...)

	// transient by contract: the installer provisions the key; the error return gives backoff
	_, err := reconciler.Reconcile(ctx, requestFromObject(directive))
	require.Error(t, err)

	sts := appsv1.StatefulSet{}
	assert.True(t, apiErrors.IsNotFound(c.Get(ctx, kube.ObjectKey(m.Namespace, fmt.Sprintf("%s-0", m.Name)), &sts)))
	assert.False(t, readDirectiveStatus(ctx, t, c, m).StsApplied)
}
