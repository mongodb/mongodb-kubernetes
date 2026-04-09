package migration

import (
	"context"
	"time"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/connectivityexit"
	"github.com/mongodb/mongodb-kubernetes/pkg/migration"
)

// ConnectivityJobResult is the return type of RunConnectivityJob. When Err is non-nil, Phase, Reason, and Message are still set for the condition.
type ConnectivityJobResult struct {
	Phase   mdbstatus.MigrationPhase
	Reason  string
	Message string
	Err     error
}

func resultRunning() ConnectivityJobResult {
	return ConnectivityJobResult{Phase: mdbstatus.MigrationPhaseConnectivityCheckRunning}
}

func resultPassed(reason, message string) ConnectivityJobResult {
	return ConnectivityJobResult{
		Phase:   mdbstatus.MigrationPhaseConnectivityCheckPassed,
		Reason:  reason,
		Message: message,
	}
}

func resultFailed(reason, message string) ConnectivityJobResult {
	return ConnectivityJobResult{
		Phase:   mdbstatus.MigrationPhaseConnectivityCheckFailed,
		Reason:  reason,
		Message: message,
	}
}

func resultErr(reason string, err error) ConnectivityJobResult {
	return ConnectivityJobResult{
		Phase:   mdbstatus.MigrationPhaseConnectivityCheckFailed,
		Reason:  reason,
		Message: err.Error(),
		Err:     err,
	}
}

// RunConnectivityJob uses a single fixed-name Job per replica set (template.Name). It Gets that Job, then:
// - No job → create from template, return Running. (No job includes the case where the Job was deleted by TTL after finishing.)
// - Job succeeded → Passed.
// - Job still running → Running.
// - Job failed → return Failed with reason/message.
// The Job template should set ttlSecondsAfterFinished so that Kubernetes deletes the Job and its Pods after completion; the next reconcile will then see no Job and create a new one for re-validation.
func RunConnectivityJob(ctx context.Context, kubeClient client.Client, template *batchv1.Job) ConnectivityJobResult {
	key := client.ObjectKey{Namespace: template.Namespace, Name: template.Name}
	var job batchv1.Job
	if err := kubeClient.Get(ctx, key, &job); err != nil {
		if !errors.IsNotFound(err) {
			return resultErr("GetJobFailed", xerrors.Errorf("getting connectivity job, err: %w", err))
		}
		toCreate := template.DeepCopy()
		if err := kubeClient.Create(ctx, toCreate); err != nil {
			return resultErr("CreateJobFailed", xerrors.Errorf("creating connectivity job, err: %w", err))
		}
		return resultRunning()
	}

	if job.Status.Succeeded > 0 {
		_, r, m := connectivityexit.NetworkConditionFromExitCode(exitcode.ExitSuccess)
		return resultPassed(r, m)
	}
	if job.Status.Failed == 0 {
		return resultRunning()
	}

	code, _ := jobPodOutcome(ctx, kubeClient, &job)
	_, r, m := connectivityexit.NetworkConditionFromExitCode(code)
	return resultFailed(r, m)
}

// jobPodOutcome lists the Job's pods once and returns the connectivity-validator container's exit code and finished time.
func jobPodOutcome(ctx context.Context, kubeClient client.Client, job *batchv1.Job) (int32, time.Time) {
	var pods corev1.PodList
	if err := kubeClient.List(ctx, &pods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{"job-name": job.Name},
	); err != nil {
		zap.S().Errorf("listing pods for job %s/%s: %v", job.Namespace, job.Name, err)
		return exitcode.ExitUnknown, time.Time{}
	}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.Name == migration.ConnectivityValidatorContainerName && cs.State.Terminated != nil {
				return cs.State.Terminated.ExitCode, cs.State.Terminated.FinishedAt.Time
			}
		}
	}
	return exitcode.ExitUnknown, time.Time{}
}
