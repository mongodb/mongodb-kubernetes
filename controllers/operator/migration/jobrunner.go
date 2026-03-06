package migration

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/migration"
)

const (
	FailedJobRetention = 5 * time.Minute
)

// ConnectivityJobResult is the return type of RunConnectivityJob. When Err is non-nil, Phase, Reason, and Message are still set for the condition.
type ConnectivityJobResult struct {
	Phase   mdbstatus.MigrationPhase
	Reason  string
	Message string
	Err     error
}

// RunConnectivityJob uses a single fixed-name Job per replica set (template.Name). It Gets that Job, then:
// - No job → create from template, return Running.
// - Job succeeded → Passed.
// - Job still running → Running.
// - Job failed → return Failed with reason/message; after FailedJobRetention, delete the job and create a new one, return Running.
func RunConnectivityJob(ctx context.Context, kubeClient client.Client, template *batchv1.Job) ConnectivityJobResult {
	key := client.ObjectKey{Namespace: template.Namespace, Name: template.Name}
	var job batchv1.Job
	if err := kubeClient.Get(ctx, key, &job); err != nil {
		if !errors.IsNotFound(err) {
			return ConnectivityJobResult{
				Phase:   mdbstatus.MigrationPhaseConnectivityCheckFailed,
				Reason:  "GetJobFailed",
				Message: err.Error(),
				Err:     fmt.Errorf("getting connectivity job: %w", err),
			}
		}
		toCreate := template.DeepCopy()
		if err := kubeClient.Create(ctx, toCreate); err != nil {
			return ConnectivityJobResult{
				Phase:   mdbstatus.MigrationPhaseConnectivityCheckFailed,
				Reason:  "CreateJobFailed",
				Message: err.Error(),
				Err:     fmt.Errorf("creating connectivity job: %w", err),
			}
		}
		return ConnectivityJobResult{Phase: mdbstatus.MigrationPhaseConnectivityCheckRunning}
	}

	if job.Status.Succeeded > 0 {
		_, r, m := migration.NetworkConditionFromExitCode(migration.ExitSuccess)
		return ConnectivityJobResult{Phase: mdbstatus.MigrationPhaseConnectivityCheckPassed, Reason: r, Message: m}
	}
	if job.Status.Failed == 0 {
		return ConnectivityJobResult{Phase: mdbstatus.MigrationPhaseConnectivityCheckRunning}
	}

	exitCode, finishedAt := jobPodOutcome(ctx, kubeClient, &job)
	_, r, m := migration.NetworkConditionFromExitCode(exitCode)
	if !finishedAt.IsZero() && time.Since(finishedAt) >= FailedJobRetention {
		if err := kubeClient.Delete(ctx, &job); err != nil && !errors.IsNotFound(err) {
			return ConnectivityJobResult{
				Phase:   mdbstatus.MigrationPhaseConnectivityCheckFailed,
				Reason:  "DeleteJobFailed",
				Message: err.Error(),
				Err:     fmt.Errorf("deleting failed connectivity job: %w", err),
			}
		}
		toCreate := template.DeepCopy()
		if err := kubeClient.Create(ctx, toCreate); err != nil {
			return ConnectivityJobResult{
				Phase:   mdbstatus.MigrationPhaseConnectivityCheckFailed,
				Reason:  "CreateJobFailed",
				Message: err.Error(),
				Err:     fmt.Errorf("creating connectivity job: %w", err),
			}
		}
		return ConnectivityJobResult{Phase: mdbstatus.MigrationPhaseConnectivityCheckRunning}
	}
	return ConnectivityJobResult{Phase: mdbstatus.MigrationPhaseConnectivityCheckFailed, Reason: r, Message: m}
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
