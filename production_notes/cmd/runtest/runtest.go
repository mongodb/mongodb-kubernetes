package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/10gen/ops-manager-kubernetes/production_notes/pkg/monitor"
	"github.com/prometheus/client_golang/api"
	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	count                       int
	waitTime                    time.Duration
	prometheusURL               string
	deployOperatorAndOpsManager bool
	deployYCSB                  bool
	opsManagerReleaseName       string
)

func parseFlags() {
	flag.IntVar(&count, "mongodb-rs-count", 1, "count of mongodb replicaset to deploy")
	flag.DurationVar(&waitTime, "time-to-wait", 2*time.Minute, "time to wait for the test to finish in minutes")
	flag.StringVar(&prometheusURL, "prometheus-url", "", "URL of prometheus server to be scrapped")
	flag.BoolVar(&deployOperatorAndOpsManager, "deploy-operator-opsmanager", false, "should deploy the operator and ops-manager")
	flag.BoolVar(&deployYCSB, "deploy-YCSB", false, "should deploy YCSB")
	flag.StringVar(&opsManagerReleaseName, "ops-manager-release-name", "", "ops/operator manager release name")
	flag.Parse()
}

// deployMongoDB deploys an instance of mongoDB replicaset
func deployMongoDB(ctx context.Context, name string) error {
	rsName := fmt.Sprintf("name=%s", name)
	opsManagerHelmReleaseName := fmt.Sprintf("opsManagerReleaseName=%s", opsManagerReleaseName)

	cmd := exec.Command("helm", "install", "--set", rsName, "--set", opsManagerHelmReleaseName, name, "../../helm_charts/mongodb/")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	log.Printf("deployed mongoDB replicaset %s: %s", name, string(output))
	return nil
}

func isOperatorReady(c kubernetes.Clientset, deploymentName, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		log.Printf("waiting for opertor deployment %s to be in ready state...", deploymentName)
		dep, err := c.AppsV1().Deployments(namespace).Get(deploymentName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return *dep.Spec.Replicas == dep.Status.ReadyReplicas, nil
	}
}

// deployOperator deploys mongodb operator instance
func deployMongoDBOperatorAndOpsManager(c kubernetes.Clientset, ctx context.Context) error {
	cmd := exec.Command("helm", "install", "om", "../../helm_charts/opsmanager/")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	log.Printf("deploying operator and ops-manager: %s", string(output))

	err = wait.PollImmediate(time.Second, 2*time.Minute, isOperatorReady(c, "mongodb-operator", "mongodb"))
	if err != nil {
		return fmt.Errorf("operator deployment couldn't reach ready state in 2 minutes")
	}
	log.Printf("deployed operator successfully...")
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

// getTimeDifferenceInSeconds returns the time difference between t2 and t1 with the assumption t2 >= t1
func getTimeDifferenceInSeconds(t1, t2 time.Time) float64 {
	return t2.Sub(t1).Seconds()
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	parseFlags()

	monitor, err := setup()
	if err != nil {
		log.Fatalf(err.Error())
	}
	monitor.Timeout = waitTime

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if deployOperatorAndOpsManager {
		if err := deployMongoDBOperatorAndOpsManager(*monitor.KubeClient, ctx); err != nil {
			log.Fatalf(err.Error())
		}
	} else {
		log.Print("skipping deploying operator and ops-manager")
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
		// TODO: make this configurable for multiple replicasets
		log.Printf("time taken by mongoDB replicaset to reach ready state: %.2f", getTimeDifferenceInSeconds(monitor.StartTime, monitor.EndTime))

		monitor.MonitorOperatorReconcileTime(ctx)
		monitor.MonitorOperatorResourceUsage(ctx)
		log.Printf("loadtesting completed...")
	case <-time.After(waitTime):
		log.Printf("timedout waiting for response...")
	}
}
