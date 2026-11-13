package operator

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
)

const (
	// restartTestUpdateRevision is the revision every member pod is expected to converge to. It is also what the mock
	// fake client reports for a StatefulSet named restartTestReplicaSetName (mock.markStatefulSetsReady appends
	// mock.stsUpdateRevisionSuffix to the StatefulSet name).
	restartTestUpdateRevision = "temple-5d9f8b7c6d"
	// restartTestOldRevision is the revision of a member that is still to be replaced.
	restartTestOldRevision = "temple-6f8e9c5d7a"
	// restartTestGoalVersion is the automation config version Ops Manager is converging to.
	restartTestGoalVersion = 42
	// restartTestReplicaSetName is the replica set the tests build; member pods and processes are named after it.
	restartTestReplicaSetName = "temple"
)

func restartTestPod(name, revision string, ready bool) corev1.Pod {
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}

	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: mock.TestNamespace,
			Labels:    map[string]string{controllerRevisionHashLabel: revision},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: readyStatus}},
		},
	}
}

func restartTestTerminatingPod(pod corev1.Pod) corev1.Pod {
	pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	return pod
}

// restartTestProcessState builds the OM view of a member. The hostnames in a ProcessStateMap are irrelevant to the
// decision: members are matched by process name.
func restartTestProcessState(processName string, goalVersionAchieved int, pingedAt time.Time) agents.ProcessState {
	state := agents.NewProcessState(processName)
	state.ProcessName = processName
	state.GoalVersionAchieved = goalVersionAchieved
	state.LastAgentPing = pingedAt

	return state
}

func restartTestReplacementExecution(automationConfigVersion int, reportedAt time.Time) *om.ProcessExecution {
	return &om.ProcessExecution{
		AutomationConfigVersion: automationConfigVersion,
		Step:                    om.ExecutionStepWaitNodeReplacement,
		ReportedAt:              reportedAt,
	}
}

// restartTestCluster builds the OM state of the three members, replacing the states for the overridden pod names.
func restartTestCluster(overrides map[string]agents.ProcessState, without ...string) agents.MongoDBClusterStateInOM {
	processStates := map[string]agents.ProcessState{}
	for _, name := range []string{"temple-0", "temple-1", "temple-2"} {
		processStates[name] = restartTestProcessState(name, restartTestGoalVersion, time.Now())
	}
	for name, state := range overrides {
		processStates[name] = state
	}
	for _, name := range without {
		delete(processStates, name)
	}

	return agents.MongoDBClusterStateInOM{GoalVersion: restartTestGoalVersion, ProcessStateMap: processStates}
}

func TestDecideNextRestart(t *testing.T) {
	now := time.Now()
	freshPing := now.Add(-time.Second)
	stalePing := now.Add(-agents.StaleProcessDuration - time.Minute)

	// rolloutInProgress is a replica set where temple-1 still runs the old revision.
	rolloutInProgress := func() []corev1.Pod {
		return []corev1.Pod{
			restartTestPod("temple-0", restartTestUpdateRevision, true),
			restartTestPod("temple-1", restartTestOldRevision, true),
			restartTestPod("temple-2", restartTestUpdateRevision, true),
		}
	}

	allOnDesiredRevision := func() []corev1.Pod {
		return []corev1.Pod{
			restartTestPod("temple-0", restartTestUpdateRevision, true),
			restartTestPod("temple-1", restartTestUpdateRevision, true),
			restartTestPod("temple-2", restartTestUpdateRevision, true),
		}
	}

	// preparedMember is how an agent reports a member right before its pod is replaced.
	preparedMember := func(processName string, execution *om.ProcessExecution) agents.ProcessState {
		state := restartTestProcessState(processName, restartTestGoalVersion-1, freshPing)
		state.Execution = execution
		return state
	}
	prepared := preparedMember("temple-1", restartTestReplacementExecution(restartTestGoalVersion, now))

	pendingPlan := restartTestProcessState("temple-0", restartTestGoalVersion, freshPing)
	pendingPlan.Plan = []string{"StartServer"}

	tests := []struct {
		name            string
		pods            []corev1.Pod
		expectedPods    int
		cluster         agents.MongoDBClusterStateInOM
		expectedDelete  string
		expectedRolling bool
		expectedUpdated int
		expectedReady   int
		expectedReason  string
	}{
		{
			name:            "no pod is waiting for its replacement",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(nil),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  restartNoWaitingReason,
		},
		{
			name:            "one prepared pod on the old revision is deleted",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": prepared}),
			expectedDelete:  "temple-1",
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "agent on pod temple-1 is waiting for node replacement",
		},
		{
			name:         "two prepared pods are never replaced together",
			pods:         rolloutInProgress(),
			expectedPods: 3,
			cluster: restartTestCluster(map[string]agents.ProcessState{
				"temple-1": prepared,
				"temple-2": preparedMember("temple-2", restartTestReplacementExecution(restartTestGoalVersion, now)),
			}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "2 pods are waiting for node replacement (temple-1, temple-2), expected exactly one",
		},
		{
			name:            "a stale report does not count as waiting",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": preparedMember("temple-1", restartTestReplacementExecution(restartTestGoalVersion, now.Add(-om.StaleProcessDuration-time.Minute)))}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  restartNoWaitingReason,
		},
		{
			name:            "a report for another automation config version does not count as waiting",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": preparedMember("temple-1", restartTestReplacementExecution(restartTestGoalVersion-1, now))}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  restartNoWaitingReason,
		},
		{
			name:            "a prepared pod without a process state does not count as waiting",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(nil, "temple-1"),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  restartNoWaitingReason,
		},
		{
			name:            "a prepared pod already on the desired revision is not deleted",
			pods:            allOnDesiredRevision(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": prepared}),
			expectedRolling: false,
			expectedUpdated: 3,
			expectedReady:   3,
			expectedReason:  "pod temple-1 is already on the desired revision",
		},
		{
			name: "a pod that is not ready blocks the replacement",
			pods: []corev1.Pod{
				restartTestPod("temple-0", restartTestUpdateRevision, true),
				restartTestPod("temple-1", restartTestOldRevision, true),
				restartTestPod("temple-2", restartTestUpdateRevision, false),
			},
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": prepared}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   2,
			expectedReason:  "only 2 of 3 pods are ready",
		},
		{
			name: "a terminating pod blocks the replacement",
			pods: []corev1.Pod{
				restartTestPod("temple-0", restartTestUpdateRevision, true),
				restartTestPod("temple-1", restartTestOldRevision, true),
				restartTestTerminatingPod(restartTestPod("temple-2", restartTestUpdateRevision, true)),
			},
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": prepared}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   2,
			expectedReason:  "only 2 of 3 pods are ready",
		},
		{
			name: "a missing pod blocks the replacement",
			pods: []corev1.Pod{
				restartTestPod("temple-0", restartTestUpdateRevision, true),
				restartTestPod("temple-1", restartTestOldRevision, true),
			},
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": prepared}),
			expectedRolling: true,
			expectedUpdated: 1,
			expectedReady:   2,
			expectedReason:  "expected 3 pods, found 2",
		},
		{
			name:            "a replaced pod with a pending plan blocks the replacement",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-0": pendingPlan, "temple-1": prepared}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "pod temple-0 has not reached the goal state in Ops Manager yet",
		},
		{
			name:            "a replaced pod behind the goal version blocks the replacement",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-0": restartTestProcessState("temple-0", restartTestGoalVersion-1, freshPing), "temple-1": prepared}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "pod temple-0 has not reached the goal state in Ops Manager yet",
		},
		{
			name:            "a replaced pod with a stale agent ping blocks the replacement",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-0": restartTestProcessState("temple-0", restartTestGoalVersion, stalePing), "temple-1": prepared}),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "pod temple-0 has not reached the goal state in Ops Manager yet",
		},
		{
			name:            "a replaced pod without a process state blocks the replacement",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": prepared}, "temple-0"),
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "pod temple-0 has not reached the goal state in Ops Manager yet",
		},
		{
			name:            "process names with the k8s/<namespace>/ prefix are matched",
			pods:            rolloutInProgress(),
			expectedPods:    3,
			cluster:         restartTestCluster(map[string]agents.ProcessState{"temple-1": preparedMember("k8s/"+mock.TestNamespace+"/temple-1", restartTestReplacementExecution(restartTestGoalVersion, now))}),
			expectedDelete:  "temple-1",
			expectedRolling: true,
			expectedUpdated: 2,
			expectedReady:   3,
			expectedReason:  "agent on pod temple-1 is waiting for node replacement",
		},
		{
			name:            "every pod is on the desired revision",
			pods:            allOnDesiredRevision(),
			expectedPods:    3,
			cluster:         restartTestCluster(nil),
			expectedRolling: false,
			expectedUpdated: 3,
			expectedReady:   3,
			expectedReason:  restartNoWaitingReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := decideNextRestart(restartTestUpdateRevision, tt.expectedPods, tt.pods, tt.cluster, now)

			if tt.expectedDelete == "" {
				assert.Nil(t, decision.PodToDelete)
			} else {
				require.NotNil(t, decision.PodToDelete)
				assert.Equal(t, tt.expectedDelete, decision.PodToDelete.Name)
			}
			assert.Equal(t, tt.expectedRolling, decision.Rolling)
			assert.Equal(t, tt.expectedUpdated, decision.UpdatedPods)
			assert.Equal(t, tt.expectedReady, decision.ReadyPods)
			assert.Equal(t, tt.expectedPods, decision.ExpectedPods)
			assert.Equal(t, tt.expectedReason, decision.Reason)
		})
	}
}

func restartTestStatefulSet(rs *mdbv1.MongoDB) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: rs.Name, Namespace: rs.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(rs.Spec.Members)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": rs.ServiceName()}},
		},
	}
}

// restartTestMemberPod returns the pod of member <index>, labelled so that it matches the StatefulSet selector.
func restartTestMemberPod(sts *appsv1.StatefulSet, index int, revision string, ready bool) corev1.Pod {
	pod := restartTestPod(fmt.Sprintf("%s-%d", sts.Name, index), revision, ready)
	for name, value := range sts.Spec.Selector.MatchLabels {
		pod.Labels[name] = value
	}

	return pod
}

// restartTestRolloutPods is a replica set whose second member still runs the old revision.
func restartTestRolloutPods(sts *appsv1.StatefulSet) []corev1.Pod {
	return []corev1.Pod{
		restartTestMemberPod(sts, 0, restartTestUpdateRevision, true),
		restartTestMemberPod(sts, 1, restartTestOldRevision, true),
		restartTestMemberPod(sts, 2, restartTestUpdateRevision, true),
	}
}

// restartTestConvergedPods is a replica set whose members all run the desired revision.
func restartTestConvergedPods(sts *appsv1.StatefulSet) []corev1.Pod {
	return []corev1.Pod{
		restartTestMemberPod(sts, 0, restartTestUpdateRevision, true),
		restartTestMemberPod(sts, 1, restartTestUpdateRevision, true),
		restartTestMemberPod(sts, 2, restartTestUpdateRevision, true),
	}
}

// restartTestMemberProcesses returns the OM state of every member at goal, except member <preparedIndex> which is
// prepared for its pod to be replaced (-1 when no member is prepared).
func restartTestMemberProcesses(rs *mdbv1.MongoDB, preparedIndex int) []om.ProcessStatus {
	processes := make([]om.ProcessStatus, 0, rs.Spec.Members)
	for i := range rs.Spec.Members {
		name := fmt.Sprintf("%s-%d", rs.Name, i)
		if i == preparedIndex {
			processes = append(processes, restartTestPreparedProcess(name))
			continue
		}
		processes = append(processes, restartTestAtGoalProcess(name))
	}

	return processes
}

func restartTestAtGoalProcess(name string) om.ProcessStatus {
	return om.ProcessStatus{Name: name, Hostname: name, LastGoalVersionAchieved: restartTestGoalVersion}
}

func restartTestPreparedProcess(name string) om.ProcessStatus {
	return om.ProcessStatus{
		Name:                    name,
		Hostname:                name,
		LastGoalVersionAchieved: restartTestGoalVersion - 1,
		Execution:               restartTestReplacementExecution(restartTestGoalVersion, time.Now()),
	}
}

// restartTestReportProcesses makes the mocked connection report the given processes with fresh agent pings.
func restartTestReportProcesses(conn *om.MockedOmConnection, agentHostnames []string, processes []om.ProcessStatus) {
	conn.ReadAutomationStatusFunc = func() (*om.AutomationStatus, error) {
		return &om.AutomationStatus{GoalVersion: restartTestGoalVersion, Processes: processes}, nil
	}
	conn.ReadAutomationAgentsFunc = func(int) (om.Paginated, error) {
		agents := make([]om.AgentStatus, 0, len(agentHostnames))
		for _, hostname := range agentHostnames {
			agents = append(agents, om.AgentStatus{Hostname: hostname, LastConf: time.Now().Format(time.RFC3339), TypeName: "AUTOMATION"})
		}

		return om.AutomationAgentStatusResponse{AutomationAgents: agents}, nil
	}
}

// restartTestReportConverged makes the mocked connection report every member registered and at goal. Needed because
// the mock's agent statuses carry no agent type, which the OM cluster state reader rejects.
func restartTestReportConverged(conn *om.MockedOmConnection, rs *mdbv1.MongoDB) {
	hostnames, _ := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), rs.Spec.Members, rs.Spec.GetExternalDomain())
	restartTestReportProcesses(conn, hostnames, restartTestMemberProcesses(rs, -1))
}

// restartTestOMConnection returns a mocked Ops Manager connection reporting the given processes with fresh agent pings.
func restartTestOMConnection(processes ...om.ProcessStatus) *om.MockedOmConnection {
	hostnames := make([]string, 0, len(processes))
	for _, process := range processes {
		hostnames = append(hostnames, process.Hostname)
	}
	conn := om.NewMockedOmConnection(nil)
	restartTestReportProcesses(conn, hostnames, processes)

	return conn
}
