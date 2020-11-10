package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/10gen/ops-manager-kubernetes/production_notes/pkg/monitor"
	"github.com/prometheus/client_golang/api"
	flag "github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	count          int
	waitTime       time.Duration
	prometheusURL  string
	deployOperator bool
)

func parseFlags() {
	flag.IntVar(&count, "mongodb-rs-count", 1, "count of mongodb replicaset to deploy")
	flag.DurationVar(&waitTime, "time-to-wait", 2*time.Minute, "time to wait for the test to finish in minutes")
	flag.StringVar(&prometheusURL, "prometheus-url", "", "URL of prometheus server to be scrapped")
	flag.BoolVar(&deployOperator, "deploy-operator", false, "should deploy the operator")
	flag.Parse()
}

// deployMongoDB deploys an instance of mongoDB replicaset
func deployMongoDB(ctx context.Context, name string) error {
	// TODO: Remove code below, used for testing atm
	// out, err := exec.Command("kubectl", "apply", "-f", "/Users/rajdeepdas/go/src/github.com/mongodb/mongodb-kubernetes-operator/deploy/crds/mongodb.com_v1_mongodb_scram_cr.yaml", "-n", "mongodb").Output()
	// if err != nil {
	// 	return err
	// }
	// log.Printf("output: %v", out)
	return nil
}

// deployOperator deploys mongodb operator instance
func deployMongoDBOperator(ctx context.Context) error {
	return nil
}

func createPrometheusClient(m *monitor.Monitor) error {
	pClient, err := api.NewClient(api.Config{
		Address: prometheusURL,
	})
	if err != nil {
		return err
	}

	m.PromClient = pClient
	return nil
}

func createKubernetesClient(m *monitor.Monitor) error {
	homePath, ok := os.LookupEnv("HOME")
	if !ok {
		return fmt.Errorf("$HOME not set")
	}

	kubeconfig := filepath.Join(
		homePath, ".kube", "config",
	)
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	m.KubeClient = client
	return nil

}

func setup() (*monitor.Monitor, error) {
	monitor := &monitor.Monitor{}

	if err := createPrometheusClient(monitor); err != nil {
		return nil, err
	}

	if err := createKubernetesClient(monitor); err != nil {
		return nil, err
	}

	return monitor, nil
}

func main() {
	parseFlags()

	monitor, err := setup()
	if err != nil {
		log.Fatalf(err.Error())
	}
	monitor.Timeout = waitTime

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if deployOperator {
		if err := deployMongoDBOperator(ctx); err != nil {
			log.Fatalf(err.Error())
		}
	}

	for i := 0; i < count; i++ {
		err = deployMongoDB(ctx, fmt.Sprintf("mongo-%d", i))
		if err != nil {
			// we don't want to proceed if we had errors deploying any of the mongodb replicasets
			log.Fatalf(err.Error())
		}
	}

	var wg sync.WaitGroup
	waitCh := make(chan struct{})

	// record the start time for deploying mongoDB replicasets
	monitor.StartTime = time.Now()
	// start monitoring of each MongoDB replicasets in it's own go-routine
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			monitor.MonitorReplicaSets(ctx, fmt.Sprintf("mongo-%d", i))
			wg.Done()
		}(i)
	}

	go func() {
		wg.Wait()
		close(waitCh)
	}()

	select {
	case <-waitCh:
		// get required metrics from the operator over the timeframe needed by mongoDB replicasets
		// to get in "ready" state.
		monitor.MonitorOperatorReconcileTime(ctx)
		monitor.MonitorOperatorResourceUsage(ctx)
		log.Printf("loadtesting completed...")
	case <-time.After(waitTime):
		log.Printf("timedout waiting for response...")
	}
}
