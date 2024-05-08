package monitor

import (
	"context"
	"log"
	"sync"
	"time"

	api "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

type Monitor struct {
	PromClient api.Client
	KubeClient *kubernetes.Clientset
	Timeout    time.Duration
	StartTime  time.Time
	EndTime    time.Time
	sync.RWMutex
}

func max(t1, t2 time.Time) time.Time {
	if t1.After(t2) {
		return t1
	}
	return t2
}

func isStatefulSetReady(c kubernetes.Clientset, stsName, namespace string) wait.ConditionWithContextFunc {
	return func(ctx context.Context) (bool, error) {
		log.Printf("waiting for statefulset %s to be in ready state...\n", stsName)

		sts, err := c.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
		// wait for the operator to create sts
		if err != nil && errors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		return sts.Status.Replicas == sts.Status.ReadyReplicas, nil
	}
}

func waitForStatefulsetReady(ctx context.Context, c kubernetes.Clientset, stsName, namespace string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, isStatefulSetReady(c, stsName, namespace))
}

// monitorStats monitors and reports the stats of 1. Reconcile time of operator, 2. Time to ready for MongoDB replicaset and 3. CPU and Memory usage of Operator
func (m *Monitor) MonitorReplicaSets(ctx context.Context, replicasetName string) {
	err := waitForStatefulsetReady(ctx, *m.KubeClient, replicasetName, "mongodb", m.Timeout)
	if err != nil {
		log.Printf("error in monitoring replicaset: %v", err)
		return
	}

	t2 := time.Now()
	m.Lock()
	m.EndTime = max(m.EndTime, t2)
	m.Unlock()
}

// MonitorOperatorReconcileTime measures the reconcile_time of the operator from the metrics being exposed
// by controller-runtime duration is the time duration over which we would like to measure the metrics,
// it's the minimum of the mongodbReplicaset becoming "ready" and the "wait" time.
func (m *Monitor) MonitorOperatorReconcileTime(ctx context.Context) {
	// Currently it only measures p50(median), the following needs to be converted into
	// a function as we measure p90, p95 etc
	queryString := "histogram_quantile(0.5, rate(controller_runtime_reconcile_time_seconds_bucket{controller=\"mongodbreplicaset-controller\"}[5m]))"

	result, err := performQuery(ctx, m.PromClient, queryString, m.StartTime, m.EndTime)
	if err != nil {
		log.Print(err.Error())
	} else {
		log.Printf("operator Reconcile time metrics p50: %v", result)
	}
}

// MonitorOperatorResourceUsage measures the operator CPU/Memory by querying the prometheus server.
// The duration over which it measures the metrics is the minimum of the "time-duration" it takes
// for the mongod Replicaset to reach a "ready" state or the specified timeout
func (m *Monitor) MonitorOperatorResourceUsage(ctx context.Context) {
	// specify pod name since we will be having only one pod corresponsing to the operator
	CPUQueryString := "sum(rate(container_cpu_usage_seconds_total{namespace=\"mongodb\", pod=~\"om-operator-.*\"}[2m])) by (pod) * 1000"

	CPUResults, err := performQuery(ctx, m.PromClient, CPUQueryString, m.StartTime, m.EndTime)
	if err != nil {
		log.Print(err.Error())
	} else {
		log.Printf("cpu resource metrics: %v", CPUResults)
	}

	MemoryQueryString := "sum(container_memory_usage_bytes{namespace=\"mongodb\", pod=~\"om-operator-.*\"}) by (pod) / 1000000 "
	MemoryResults, err := performQuery(ctx, m.PromClient, MemoryQueryString, m.StartTime, m.EndTime)
	if err != nil {
		log.Print(err.Error())
	} else {
		log.Printf("memory Resource metrics: %v", MemoryResults)
	}
}

func performQuery(ctx context.Context, promClient api.Client, queryString string, s time.Time, e time.Time) (model.Value, error) {
	v1api := v1.NewAPI(promClient)

	r := v1.Range{
		Start: s,
		End:   e,
		Step:  time.Minute,
	}

	results, warnings, err := v1api.QueryRange(ctx, queryString, r)
	if err != nil {
		log.Printf("Error querying Prometheus: %v\n", err)
		return nil, err
	}

	if len(warnings) > 0 {
		log.Printf("Warnings: %v\n", warnings)
	}
	// TODO: Persist the result, upload this to S3(or something) when we increase the replicaset count
	return results, nil
}
