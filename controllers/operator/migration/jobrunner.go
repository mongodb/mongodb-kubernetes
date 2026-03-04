package migration

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/migration"
)

// RunConnectivityJob creates the connectivity-validator Job if it does not yet exist,
// then inspects the Job's current status and returns the appropriate MigrationPhase.
//
// Callers should inspect the returned phase:
//   - ConnectivityCheckRunning: requeue after ~30 s and check again.
//   - ConnectivityCheckPassed:  all external members are reachable; proceed with migration.
//   - ConnectivityCheckFailed:  see the returned reason/message; requeue after ~5 min.
//
// A non-nil error is returned only for unexpected Kubernetes API failures.
func RunConnectivityJob(ctx context.Context, kubeClient client.Client, job *batchv1.Job) (phase mdbstatus.MigrationPhase, reason, message string, err error) {
	existing := &batchv1.Job{}
	getErr := kubeClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, existing)

	if apierrors.IsNotFound(getErr) {
		if createErr := kubeClient.Create(ctx, job); createErr != nil {
			return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "",
				fmt.Errorf("creating connectivity-validator job: %w", createErr)
		}
		return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "", nil
	}
	if getErr != nil {
		return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "",
			fmt.Errorf("getting connectivity-validator job: %w", getErr)
	}

	// Job exists — check outcome.
	if existing.Status.Succeeded > 0 {
		_, r, m := migration.NetworkConditionFromExitCode(migration.ExitSuccess)
		return mdbstatus.MigrationPhaseConnectivityCheckPassed, r, m, nil
	}
	if existing.Status.Failed > 0 {
		exitCode := jobContainerExitCode(ctx, kubeClient, existing)
		_, r, m := migration.NetworkConditionFromExitCode(exitCode)
		return mdbstatus.MigrationPhaseConnectivityCheckFailed, r, m, nil
	}

	// Job is still active (running).
	return mdbstatus.MigrationPhaseConnectivityCheckRunning, "", "", nil
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
