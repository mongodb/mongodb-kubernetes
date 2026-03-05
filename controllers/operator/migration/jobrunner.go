package migration

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/migration"
)

const (
	ConnectivityCheckReplicaSetLabel = "mongodb.k8s.io/connectivity-check-replica-set"
	ConnectivityCheckDryRunLabel     = "mongodb.k8s.io/connectivity-check-dry-run"
	OperatorManagedByLabel           = "app.kubernetes.io/managed-by"
	OperatorManagedByValue           = "mongodb-kubernetes-operator"
	FailedJobRetention               = 5 * time.Minute
)

// RunConnectivityJob lists connectivity-check Jobs for this replica set, then:
// - If any Job succeeded → Passed.
// - If any Job is still running → Running.
// - Else (none or all failed): create a new Job if none exist or the most recent failure was FailedJobRetention ago; return Failed.
// Failed Jobs are never deleted so logs remain for debugging.
func RunConnectivityJob(ctx context.Context, kubeClient client.Client, template *batchv1.Job) (phase mdbstatus.MigrationPhase, reason, message string, err error) {
	replicaSetName := strings.TrimSuffix(template.Name, "-connectivity-check")

	var list batchv1.JobList
	if err := kubeClient.List(ctx, &list,
		client.InNamespace(template.Namespace),
		client.MatchingLabels{ConnectivityCheckReplicaSetLabel: replicaSetName},
	); err != nil {
		return mdbstatus.MigrationPhaseConnectivityCheckFailed, "", "", fmt.Errorf("listing connectivity jobs: %w", err)
	}

	jobs := list.Items
	for i := range jobs { // TODO: check that logic
		if jobs[i].Status.Succeeded > 0 {
			_, r, m := migration.NetworkConditionFromExitCode(migration.ExitSuccess)
			return mdbstatus.MigrationPhaseConnectivityCheckPassed, r, m, nil
		}
		if jobs[i].Status.Failed == 0 {
			return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "", nil
		}
	}

	// None or all failed: maybe create a new run, then return Failed
	var exitCode int32 = migration.ExitUnknown
	var finishedAt time.Time
	if j := newestJob(jobs); j != nil {
		exitCode, finishedAt = jobPodOutcome(ctx, kubeClient, j)
	}
	_, r, m := migration.NetworkConditionFromExitCode(exitCode)

	shouldCreate := len(jobs) == 0 || (!finishedAt.IsZero() && time.Since(finishedAt) >= FailedJobRetention)
	if shouldCreate {
		toCreate := newUniqueJob(template, replicaSetName)
		if err := kubeClient.Create(ctx, toCreate); err != nil {
			return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "", nil
		}
	}
	return mdbstatus.MigrationPhaseConnectivityCheckFailed, r, m, nil
}

// newestJob returns the Job in the slice with the latest CreationTimestamp, or nil if empty.
func newestJob(jobs []batchv1.Job) *batchv1.Job {
	if len(jobs) == 0 {
		return nil
	}
	out := &jobs[0]
	for i := 1; i < len(jobs); i++ {
		if jobs[i].CreationTimestamp.After(out.CreationTimestamp.Time) {
			out = &jobs[i]
		}
	}
	return out
}

// newUniqueJob returns a copy of the template with a unique name (timestamp suffix) and labels set.
func newUniqueJob(template *batchv1.Job, replicaSetName string) *batchv1.Job {
	j := template.DeepCopy()
	j.Name = template.Name + "-" + strconv.FormatInt(time.Now().Unix(), 10)
	j.ResourceVersion = ""
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[ConnectivityCheckReplicaSetLabel] = replicaSetName
	j.Labels[ConnectivityCheckDryRunLabel] = "true"
	j.Labels[OperatorManagedByLabel] = OperatorManagedByValue
	return j
}

// jobPodOutcome lists the Job's pods once and returns the connectivity-validator container's exit code and finished time.
func jobPodOutcome(ctx context.Context, kubeClient client.Client, job *batchv1.Job) (exitCode int32, finishedAt time.Time) {
	var pods corev1.PodList
	if err := kubeClient.List(ctx, &pods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{"job-name": job.Name},
	); err != nil {
		return migration.ExitUnknown, time.Time{}
	}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.Name == "connectivity-validator" && cs.State.Terminated != nil {
				return cs.State.Terminated.ExitCode, cs.State.Terminated.FinishedAt.Time
			}
		}
	}
	return migration.ExitUnknown, time.Time{}
}
