package migration

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/migration"
)

// ConnectivityCheckReplicaSetLabel is set on connectivity-check Jobs so we can list them by replica set.
// Value is the MongoDB replica set name (e.g. "my-rs"). Set by the controller when building the Job.
const ConnectivityCheckReplicaSetLabel = "mongodb.k8s.io/connectivity-check-replica-set"

// ConnectivityCheckDryRunLabel marks the Job as part of the migration dry run for the MongoDB resource.
// Value is "true". Use with OperatorManagedByLabel to select connectivity-check Jobs for deletion.
const ConnectivityCheckDryRunLabel = "mongodb.k8s.io/connectivity-check-dry-run"

// OperatorManagedByLabel is the standard app.kubernetes.io/managed-by label; value is the operator name.
const OperatorManagedByLabel = "app.kubernetes.io/managed-by"
const OperatorManagedByValue = "mongodb-kubernetes-operator"

// FailedJobRetention is how long we keep a failed connectivity Job (and its pod) before
// creating a new attempt, so the customer can inspect logs (e.g. kubectl logs).
const FailedJobRetention = 5 * time.Minute

// RunConnectivityJob creates the connectivity-validator Job if needed (with a unique name per run),
// then inspects the latest Job's status and returns the appropriate MigrationPhase.
// Failed jobs are never deleted; after FailedJobRetention we create a new Job with a new unique name.
//
// Callers should inspect the returned phase:
//   - ConnectivityCheckRunning: requeue after ~30 s and check again.
//   - ConnectivityCheckPassed:  all external members are reachable; proceed with migration.
//   - ConnectivityCheckFailed:  see the returned reason/message; requeue after ~5 min.
//
// A non-nil error is returned only for unexpected Kubernetes API failures.
func RunConnectivityJob(ctx context.Context, kubeClient client.Client, job *batchv1.Job) (phase mdbstatus.MigrationPhase, reason, message string, err error) {
	replicaSetName := strings.TrimSuffix(job.Name, "-connectivity-check")

	list := &batchv1.JobList{}
	if listErr := kubeClient.List(ctx, list,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{ConnectivityCheckReplicaSetLabel: replicaSetName},
	); listErr != nil {
		return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "",
			fmt.Errorf("listing connectivity jobs: %w", listErr)
	}

	// Latest job by creation time (newest first)
	jobs := list.Items
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[j].CreationTimestamp.Before(&jobs[i].CreationTimestamp)
	})

	if len(jobs) == 0 {
		// No job yet — create one with a unique name.
		toCreate := job.DeepCopy()
		toCreate.Name = job.Name + "-" + strconv.FormatInt(time.Now().Unix(), 10)
		toCreate.ResourceVersion = ""
		ensureLabel(toCreate, ConnectivityCheckReplicaSetLabel, replicaSetName)
		ensureLabel(toCreate, ConnectivityCheckDryRunLabel, "true")
		ensureLabel(toCreate, OperatorManagedByLabel, OperatorManagedByValue)
		if createErr := kubeClient.Create(ctx, toCreate); createErr != nil {
			return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "",
				fmt.Errorf("creating connectivity-validator job: %w", createErr)
		}
		return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "", nil
	}

	latest := &jobs[0]

	if latest.Status.Succeeded > 0 {
		_, r, m := migration.NetworkConditionFromExitCode(migration.ExitSuccess)
		return mdbstatus.MigrationPhaseConnectivityCheckPassed, r, m, nil
	}
	if latest.Status.Failed > 0 {
		exitCode := jobContainerExitCode(ctx, kubeClient, latest)
		_, r, m := migration.NetworkConditionFromExitCode(exitCode)

		// After FailedJobRetention, create a new Job with a unique name (old one stays for logs).
		if failedLongEnough(ctx, kubeClient, latest) {
			toCreate := job.DeepCopy()
			toCreate.Name = job.Name + "-" + strconv.FormatInt(time.Now().Unix(), 10)
			toCreate.ResourceVersion = ""
			ensureLabel(toCreate, ConnectivityCheckReplicaSetLabel, replicaSetName)
			ensureLabel(toCreate, ConnectivityCheckDryRunLabel, "true")
			ensureLabel(toCreate, OperatorManagedByLabel, OperatorManagedByValue)
			if createErr := kubeClient.Create(ctx, toCreate); createErr != nil {
				return mdbstatus.MigrationPhaseConnectivityCheckFailed, r, m,
					fmt.Errorf("creating connectivity-validator job: %w", createErr)
			}
		}

		return mdbstatus.MigrationPhaseConnectivityCheckFailed, r, m, nil
	}

	// Job is still active (running).
	return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "", nil
}

func ensureLabel(job *batchv1.Job, key, value string) {
	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
	job.Labels[key] = value
}

// failedLongEnough returns true if the Job has been failed for at least FailedJobRetention.
// Failed Jobs often have nil CompletionTime; we use the connectivity-validator container's
// termination time from the pod instead.
func failedLongEnough(ctx context.Context, kubeClient client.Client, job *batchv1.Job) bool {
	finishedAt := jobPodFailureTime(ctx, kubeClient, job)
	if finishedAt.IsZero() {
		return false
	}
	return time.Since(finishedAt) >= FailedJobRetention
}

// jobPodFailureTime returns when the connectivity-validator container terminated in the Job's
// pods, or zero time if not found. Uses the first pod with a terminated container.
func jobPodFailureTime(ctx context.Context, kubeClient client.Client, job *batchv1.Job) time.Time {
	pods := &corev1.PodList{}
	if err := kubeClient.List(ctx, pods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{"job-name": job.Name},
	); err != nil {
		return time.Time{}
	}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.Name == "connectivity-validator" && cs.State.Terminated != nil {
				return cs.State.Terminated.FinishedAt.Time
			}
		}
	}
	return time.Time{}
}

// jobContainerExitCode looks up the exit code of the connectivity-validator container
// from the pods owned by job. Returns migration.ExitUnknown when the code cannot be
// determined (e.g. the pod has not yet written a termination status).
func jobContainerExitCode(ctx context.Context, kubeClient client.Client, job *batchv1.Job) int32 {
	pods := &corev1.PodList{}
	if err := kubeClient.List(ctx, pods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{"job-name": job.Name},
	); err != nil {
		return migration.ExitUnknown
	}

	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.Name == "connectivity-validator" && cs.State.Terminated != nil {
				return cs.State.Terminated.ExitCode
			}
		}
	}
	return migration.ExitUnknown
}
