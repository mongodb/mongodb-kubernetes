package migration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes/cmd/connectivity-validator/exitcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/migration"
)

const (
	namespace = "test-ns"
	jobName   = "my-rs-connectivity-check"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

func testTemplate() *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: migration.ConnectivityValidatorContainerName}},
				},
			},
		},
	}
}

// conditionFromResult returns the condition that would be set on the MongoDB status for this result.
func conditionFromResult(r ConnectivityJobResult) mdbstatus.Option {
	return mdbstatus.NewMigrationConditionOption(mdbstatus.MigrationCondition(r.Phase, r.Reason, r.Message))
}

func TestRunConnectivityJob_StateMachine_NoJobCreatesAndReturnsRunning(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	template := testTemplate()

	result := RunConnectivityJob(ctx, kubeClient, template)

	assert.NoError(t, result.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, result.Phase, "phase: Running when job was just created")
	// Condition for Running is Unknown
	opt := conditionFromResult(result)
	c := opt.(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c.Status, "condition status: Unknown while running")

	// Job should exist in the cluster
	var job batchv1.Job
	err := kubeClient.Get(ctx, client.ObjectKeyFromObject(template), &job)
	require.NoError(t, err)
	assert.Equal(t, jobName, job.Name)
}

func TestRunConnectivityJob_StateMachine_JobRunningReturnsRunning(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Status:     batchv1.JobStatus{Succeeded: 0, Failed: 0},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build()
	template := testTemplate()

	result := RunConnectivityJob(ctx, kubeClient, template)

	assert.NoError(t, result.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, result.Phase)
	opt := conditionFromResult(result)
	c := opt.(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c.Status)
}

func TestRunConnectivityJob_StateMachine_JobSucceededReturnsPassed(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(time.Now()), // recent so not stale
		},
		Status: batchv1.JobStatus{Succeeded: 1, Failed: 0},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).Build()
	template := testTemplate()

	result := RunConnectivityJob(ctx, kubeClient, template)

	assert.NoError(t, result.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckPassed, result.Phase)
	assert.Equal(t, "NetworkValidationPassed", result.Reason)
	opt := conditionFromResult(result)
	c := opt.(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionTrue, c.Status, "condition: True when passed")
}

func TestRunConnectivityJob_StateMachine_JobFailedRecentReturnsFailedNoReplace(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	now := time.Now()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Status:     batchv1.JobStatus{Succeeded: 0, Failed: 1},
	}
	// Pod with recent failure (1 min ago)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName + "-pod",
			Namespace: namespace,
			Labels:    map[string]string{"job-name": jobName},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: migration.ConnectivityValidatorContainerName,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode:   exitcode.ExitNetworkFailed,
						FinishedAt: metav1.NewTime(now.Add(-1 * time.Minute)),
					},
				},
			}},
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod).Build()
	template := testTemplate()

	result := RunConnectivityJob(ctx, kubeClient, template)

	assert.NoError(t, result.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckFailed, result.Phase)
	assert.Equal(t, "NetworkFailed", result.Reason)
	opt := conditionFromResult(result)
	c := opt.(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionFalse, c.Status, "condition: False when failed")

	// Job should still exist (we did not delete/replace)
	var still batchv1.Job
	err := kubeClient.Get(ctx, client.ObjectKeyFromObject(job), &still)
	require.NoError(t, err)
}

// TestRunConnectivityJob_StateMachine_MultipleReconciles simulates several reconciles in sequence and asserts phase/condition transitions.
func TestRunConnectivityJob_StateMachine_MultipleReconciles(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	template := testTemplate()

	// Reconcile 1: No job → create → Running, Condition Unknown
	r1 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r1.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, r1.Phase, "reconcile 1: Running after create")
	c1 := conditionFromResult(r1).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c1.Status)

	// Reconcile 2: Job still active (no status update in fake) → still Running
	r2 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r2.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, r2.Phase, "reconcile 2: Running while job active")
	c2 := conditionFromResult(r2).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c2.Status)

	// Simulate job just succeeded with recent CreationTimestamp (fake may not allow updating CreationTimestamp, so replace)
	var oldJob batchv1.Job
	require.NoError(t, kubeClient.Get(ctx, client.ObjectKeyFromObject(template), &oldJob))
	require.NoError(t, kubeClient.Delete(ctx, &oldJob))
	freshSucceeded := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              jobName,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Status: batchv1.JobStatus{Succeeded: 1, Failed: 0},
	}
	require.NoError(t, kubeClient.Create(ctx, freshSucceeded))
	require.NoError(t, kubeClient.Status().Update(ctx, freshSucceeded))

	// Reconcile 3: Job succeeded → Passed, Condition True
	r3 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r3.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckPassed, r3.Phase, "reconcile 3: Passed after job succeeds")
	c3 := conditionFromResult(r3).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionTrue, c3.Status)
	assert.Equal(t, "NetworkValidationPassed", r3.Reason)
}

// TestRunConnectivityJob_StateMachine_MultipleReconciles_Failure runs several reconciles through the failure path and retry.
func TestRunConnectivityJob_StateMachine_MultipleReconciles_Failure(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	template := testTemplate()
	now := time.Now()

	// Reconcile 1: No job → create → Running, Condition Unknown
	r1 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r1.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, r1.Phase, "reconcile 1: Running after create")
	c1 := conditionFromResult(r1).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c1.Status)

	// Mark job as failed and add a pod with recent finishedAt (1 min ago)
	var job batchv1.Job
	require.NoError(t, kubeClient.Get(ctx, client.ObjectKeyFromObject(template), &job))
	job.Status.Failed = 1
	require.NoError(t, kubeClient.Status().Update(ctx, &job))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName + "-pod",
			Namespace: namespace,
			Labels:    map[string]string{"job-name": jobName},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: migration.ConnectivityValidatorContainerName,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode:   exitcode.ExitNetworkFailed,
						FinishedAt: metav1.NewTime(now.Add(-1 * time.Minute)),
					},
				},
			}},
		},
	}
	require.NoError(t, kubeClient.Create(ctx, pod))

	// Reconcile 2: Job failed (recent) → Failed, Condition False, no replace
	r2 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r2.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckFailed, r2.Phase, "reconcile 2: Failed when job failed recently")
	c2 := conditionFromResult(r2).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionFalse, c2.Status)
	assert.Equal(t, "NetworkFailed", r2.Reason)

	// Simulate TTL: Job was deleted by Kubernetes after it finished. Next reconcile will create a new one.
	require.NoError(t, kubeClient.Get(ctx, client.ObjectKeyFromObject(template), &job))
	require.NoError(t, kubeClient.Delete(ctx, &job))
	require.NoError(t, kubeClient.Delete(ctx, pod))

	// Reconcile 3: No job (TTL deleted it) → create new job → Running, Condition Unknown
	r3 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r3.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, r3.Phase, "reconcile 3: Running after TTL deleted job")
	c3 := conditionFromResult(r3).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c3.Status)

	// Reconcile 4: New job still active → Running
	r4 := RunConnectivityJob(ctx, kubeClient, template)
	require.NoError(t, r4.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckRunning, r4.Phase, "reconcile 4: Running while new job active")
	c4 := conditionFromResult(r4).(mdbstatus.MigrationConditionOption).Condition
	assert.Equal(t, metav1.ConditionUnknown, c4.Status)
}

func TestRunConnectivityJob_StateMachine_GetJobFailsReturnsFailed(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)
	realClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	template := testTemplate()
	wrapper := &getFailingClient{Client: realClient}
	result := RunConnectivityJob(ctx, wrapper, template)

	assert.Error(t, result.Err)
	assert.Equal(t, mdbstatus.MigrationPhaseConnectivityCheckFailed, result.Phase)
	assert.Equal(t, "GetJobFailed", result.Reason)
}

// getFailingClient wraps a client and returns an error on Get (to simulate API failure).
type getFailingClient struct {
	client.Client
}

func (c *getFailingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return fmt.Errorf("simulated get failure")
}
