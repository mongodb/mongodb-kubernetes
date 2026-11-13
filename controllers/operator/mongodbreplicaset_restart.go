package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

// controllerRevisionHashLabel is set by Kubernetes on every pod of a StatefulSet to the revision it was created from.
const controllerRevisionHashLabel = "controller-revision-hash"

// restartNoWaitingReason is reported when no member has prepared its process for a pod replacement.
const restartNoWaitingReason = "no pod is waiting for node replacement"

// restartDecision is the outcome of one restart coordination pass: at most one member pod is replaced per pass.
type restartDecision struct {
	PodToDelete  *corev1.Pod
	Rolling      bool
	UpdatedPods  int
	ReadyPods    int
	ExpectedPods int
	Reason       string
}

// decideNextRestart picks the single member pod that may be deleted now. static-ga member pods are only ever
// replaced by the operator, so a pod is deleted only once its agent has parked the process and every other member
// is healthy and already converged, which keeps the replica set at most one member down.
func decideNextRestart(updateRevision string, expectedPods int, pods []corev1.Pod, cluster agents.MongoDBClusterStateInOM, now time.Time) restartDecision {
	// A pod's process is named either after the pod or with the k8s/<namespace> prefix, depending on the agent.
	stateByProcessName := make(map[string]agents.ProcessState, len(cluster.ProcessStateMap))
	for _, state := range cluster.ProcessStateMap {
		if state.ProcessName != "" {
			stateByProcessName[state.ProcessName] = state
		}
	}
	stateForPod := func(pod *corev1.Pod) (agents.ProcessState, bool) {
		if state, ok := stateByProcessName[pod.Name]; ok {
			return state, true
		}
		state, ok := stateByProcessName[process.PodNameToProcessName(pod.Name, pod.Namespace)]
		return state, ok
	}

	decision := restartDecision{ExpectedPods: expectedPods}
	preparedPodIndex := -1
	var preparedPods []string
	for i := range pods {
		pod := &pods[i]
		if pod.Labels[controllerRevisionHashLabel] == updateRevision {
			decision.UpdatedPods++
		} else {
			decision.Rolling = true
		}
		if podIsReady(pod) {
			decision.ReadyPods++
		}
		if state, ok := stateForPod(pod); ok && state.Execution.IsWaitingForNodeReplacement(cluster.GoalVersion, now) {
			preparedPodIndex = i
			preparedPods = append(preparedPods, pod.Name)
		}
	}

	if preparedPodIndex < 0 {
		decision.Reason = restartNoWaitingReason
		return decision
	}
	if len(preparedPods) > 1 {
		// The agents park one process at a time; more than one means we cannot tell which replacement is safe.
		decision.Reason = fmt.Sprintf("%d pods are waiting for node replacement (%s), expected exactly one", len(preparedPods), strings.Join(preparedPods, ", "))
		return decision
	}

	prepared := &pods[preparedPodIndex]
	if prepared.Labels[controllerRevisionHashLabel] == updateRevision {
		decision.Reason = fmt.Sprintf("pod %s is already on the desired revision", prepared.Name)
		return decision
	}
	if len(pods) != expectedPods {
		decision.Reason = fmt.Sprintf("expected %d pods, found %d", expectedPods, len(pods))
		return decision
	}
	if decision.ReadyPods != len(pods) {
		decision.Reason = fmt.Sprintf("only %d of %d pods are ready", decision.ReadyPods, len(pods))
		return decision
	}

	for i := range pods {
		pod := &pods[i]
		if pod.Labels[controllerRevisionHashLabel] != updateRevision {
			continue
		}
		state, ok := stateForPod(pod)
		if !ok || state.GoalVersionAchieved < cluster.GoalVersion || len(state.Plan) != 0 || state.IsStale() {
			decision.Reason = fmt.Sprintf("pod %s has not reached the goal state in Ops Manager yet", pod.Name)
			return decision
		}
	}

	decision.PodToDelete = prepared
	decision.Reason = fmt.Sprintf("agent on pod %s is waiting for node replacement", prepared.Name)
	return decision
}

func podIsReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// coordinateRestarts deletes at most one member pod per reconcile, and only once its agent has prepared the process
// for the replacement. Until every member runs the update revision the reconcile reports progress and requeues.
func (r *ReplicaSetReconcilerHelper) coordinateRestarts(ctx context.Context, conn om.Connection, expectedGeneration int64) workflow.Status {
	rs := r.resource
	log := r.log

	revision, err := statefulset.GetUpdateRevision(ctx, rs.Namespace, rs.Name, &expectedGeneration, r.reconciler.client)
	if err != nil {
		return workflow.Failed(err)
	}
	if revision == "" {
		return workflow.Pending("StatefulSet %s revision is not available yet", rs.Name).WithRetry(3)
	}

	sts, err := r.reconciler.client.GetStatefulSet(ctx, rs.ObjectKey())
	if err != nil {
		return workflow.Failed(err)
	}
	pods := &corev1.PodList{}
	if err := r.reconciler.client.List(ctx, pods, client.InNamespace(rs.Namespace), client.MatchingLabels(sts.Spec.Selector.MatchLabels)); err != nil {
		return workflow.Failed(err)
	}

	cluster, err := agents.GetMongoDBClusterState(ctx, conn)
	if err != nil {
		return workflow.Failed(err)
	}

	decision := decideNextRestart(revision, int(*sts.Spec.Replicas), pods.Items, cluster, time.Now())
	switch {
	case decision.Rolling:
		r.rolloutStatus = workflow.Pending("StatefulSet %s is rolling out revision %s: %d/%d pods updated", rs.Name, revision, decision.UpdatedPods, decision.ExpectedPods).WithRetry(10)
	case decision.ReadyPods != decision.ExpectedPods:
		return workflow.Pending("StatefulSet %s: not all pods are ready (wanted: %d, ready: %d)", rs.Name, decision.ExpectedPods, decision.ReadyPods).WithRetry(3)
	}

	if decision.PodToDelete == nil {
		// Only a rollout in progress is worth an Info line; at steady state this runs on every reconcile.
		if decision.Rolling {
			log.Infof("Not restarting a member of replica set %s: %s", rs.Name, decision.Reason)
		}
		return workflow.OK()
	}

	pod := decision.PodToDelete
	log.Infof("Restarting pod %s of replica set %s on revision %s: %s", pod.Name, rs.Name, revision, decision.Reason)
	// The UID precondition keeps a pod that was recreated in the meantime from being deleted.
	if err := r.reconciler.client.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}); err != nil {
		if errors.IsNotFound(err) {
			log.Infof("Pod %s was already deleted: %s", pod.Name, err)
			return workflow.OK()
		}
		if errors.IsConflict(err) {
			log.Infof("Pod %s was replaced while it was being restarted: %s", pod.Name, err)
			return workflow.Pending("Pod %s was replaced while it was being restarted", pod.Name).WithRetry(3)
		}
		return workflow.Failed(xerrors.Errorf("failed to delete pod %s: %w", pod.Name, err))
	}

	return workflow.OK()
}
