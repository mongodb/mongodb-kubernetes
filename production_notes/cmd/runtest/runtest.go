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
	"github.com/10gen/ops-manager-kubernetes/production_notes/pkg/ycsb"
	"github.com/prometheus/client_golang/api"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
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
	tlsEnabled                  bool
	cleanupResources            bool
)

func parseFlags() {
	flag.IntVar(&count, "mongodb-rs-count", 1, "count of mongodb replicaset to deploy")
	flag.DurationVar(&waitTime, "time-to-wait", 2*time.Minute, "time to wait for the test to finish in minutes")
	flag.StringVar(&prometheusURL, "prometheus-url", "", "URL of prometheus server to be scrapped")
	flag.BoolVar(&deployOperatorAndOpsManager, "deploy-operator-opsmanager", false, "should deploy the operator and ops-manager")
	flag.BoolVar(&deployYCSB, "deploy-YCSB", false, "should deploy YCSB")
	flag.BoolVar(&tlsEnabled, "tls", false, "enables TLS for MongoDB")
	flag.StringVar(&opsManagerReleaseName, "ops-manager-release-name", "", "ops/operator manager release name")
	flag.BoolVar(&cleanupResources, "cleanup", false, "cleanup installed resources on successful completion")
	flag.Parse()
}

// deployMongoDB deploys an instance of mongoDB replicaset
func deployMongoDB(ctx context.Context, name string, certsName string, opsManagerCredentials map[string]string) error {
	rsName := fmt.Sprintf("name=%s", name)
	opsManagerHelmReleaseName := fmt.Sprintf("opsManagerReleaseName=%s", opsManagerReleaseName)
	apikey := fmt.Sprintf("opsManager.APIKey=%s", opsManagerCredentials["user"])
	apisecret := fmt.Sprintf("opsManager.APISecret=%s", opsManagerCredentials["publicApiKey"])
	tlsSecretRef := fmt.Sprintf("security.tls.secretRef.name=%s", certsName)
	tlsEnabled := fmt.Sprintf("security.tls.enabled=%t", tlsEnabled)

	cmd := exec.Command("helm", "install", "-f", "../../helm_charts/mongodb/values.yaml", "--set", rsName, "--set", opsManagerHelmReleaseName, "--set", apikey, "--set", apisecret, "--set", tlsSecretRef, "--set", tlsEnabled, name, "../../helm_charts/mongodb/replicaSet")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", output)
	}

	log.Printf("deployed mongoDB replicaset %s: %s", name, string(output))
	return nil
}

func getSecretStringData(c kubernetes.Clientset, secretName string) (map[string]string, error) {
	stringData := map[string]string{}
	secret, err := c.CoreV1().Secrets("mongodb").Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil {
		return stringData, fmt.Errorf("can't get secret %s: %s", secretName, err)
	}
	for key, value := range secret.Data {
		stringData[key] = string(value)
	}
	return stringData, nil
}

// createTLSCerts prepares the TLS certs for the MongoDB Deployment
func createTLSCerts(c kubernetes.Clientset, replicaSetName string, releaseName string) error {
	rsName := fmt.Sprintf("name=%s", replicaSetName)

	cmd := exec.Command("helm", "install", "-f", "../../helm_charts/mongodb/values.yaml", "--set", rsName, releaseName, "../../helm_charts/mongodb/certs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", output)
	}

	log.Printf("Created tls certs for replica set %s under release name %s: %s", replicaSetName, releaseName, string(output))

	secretNames := make([]string, 3)
	for i := 0; i < 3; i++ {
		secretNames[i] = fmt.Sprintf("%s-%d-abcd", replicaSetName, i)
	}

	err = wait.PollImmediate(time.Second, time.Minute, areSecretsCreated(c, "mongodb", secretNames...))
	if err != nil {
		return fmt.Errorf("secrets weren't created within 1 minute: %s", err)
	}

	// Need to read the secrets one by one and create a new one which contains
	// the concatenation of the generated key and crt
	stringData := map[string]string{}
	data := map[string][]byte{}

	for i := 0; i < 3; i++ {
		secretStringData, err := getSecretStringData(c, secretNames[i])
		if err != nil {
			return fmt.Errorf("can't read secret data: %s", err)
		}
		stringData[fmt.Sprintf("%s-%d-pem", replicaSetName, i)] = secretStringData["tls.key"] + secretStringData["tls.crt"]
	}

	for s, s2 := range stringData {
		data[s] = []byte(s2)
	}
	_, err = c.CoreV1().Secrets("mongodb").Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      releaseName,
			Namespace: "mongodb",
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "runtest"},
		},
		Data: data}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("can't create secret: %s", err)
	}
	return nil
}

func isOperatorReady(c kubernetes.Clientset, deploymentName, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		log.Printf("waiting for operator deployment %s to be in ready state...", deploymentName)
		dep, err := c.AppsV1().Deployments(namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return *dep.Spec.Replicas == dep.Status.ReadyReplicas, nil
	}
}

func areSecretsCreated(c kubernetes.Clientset, namespace string, names ...string) wait.ConditionFunc {
	return func() (bool, error) {
		log.Printf("waiting for all secrets %v to be created...", names)
		for _, name := range names {
			_, err := c.CoreV1().Secrets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
		}
		return true, nil
	}
}

// deployOperator deploys mongodb operator instance
func deployMongoDBOperatorAndOpsManager(c kubernetes.Clientset, ctx context.Context) error {
	cmd := exec.Command("helm", "install", "om", "../../helm_charts/opsmanager/")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", output)
	}
	log.Printf("deploying operator and ops-manager: %s", string(output))

	err = wait.PollImmediate(time.Second, 2*time.Minute, isOperatorReady(c, "om-operator", "mongodb"))
	if err != nil {
		return fmt.Errorf("operator deployment couldn't reach ready state in 2 minutes: %s", err)
	}
	log.Printf("deployed operator successfully...")
	return nil
}

func hasYCSBJobCompleted(c kubernetes.Clientset, jobName, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		log.Printf("waiting for ycsb job %s to complete...", jobName)
		job, err := c.BatchV1().Jobs(namespace).Get(context.TODO(), jobName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return job.Status.Succeeded == 1, nil
	}
}

// DeployYCSB deploys ycsb as a job to loadtest mongoDB
func deployYCSBJob(ctx context.Context, c kubernetes.Clientset, mongoDBName string) error {
	binding := fmt.Sprintf("binding=%s-binding", mongoDBName)
	tlsEnabled := fmt.Sprintf("tls=%t", tlsEnabled)

	cmd := exec.Command("helm", "install", "ycsb", "--set", binding, "--set", tlsEnabled, "../../helm_charts/ycsb/")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", output)
	}

	log.Printf("deploying ycsb: %s", string(output))

	err = wait.PollImmediate(time.Second, 2*time.Minute, hasYCSBJobCompleted(c, "ycsb-ycsb-job", "mongodb"))
	if err != nil {
		return fmt.Errorf("ycsb job not completed successfully")
	}
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

// cleanupTLSSecrets ensures that all old secrets from previous runs with TLS enabled are deleted
func cleanupTLSSecrets(c kubernetes.Clientset) error {
	// Delete all the certs-x
	err := c.CoreV1().Secrets("mongodb").DeleteCollection(context.TODO(), metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=runtest",
	})
	if err != nil {
		return err
	}

	// Delete all the automatically generated secrets:
	return c.CoreV1().Secrets("mongodb").DeleteCollection(context.TODO(), metav1.DeleteOptions{}, metav1.ListOptions{
		FieldSelector: "type=kubernetes.io/tls",
		// This is a bit more robust than deleting by name, as we don't have to worry if
		// in the future we change the templated name. And we do not have any other
		// secret in our testing with this type
	})

}

func cleanMongoDBResource(c kubernetes.Clientset, mongoReleaseName string) error {
	err := helmUninstall(mongoReleaseName)
	if err != nil {
		return err
	}
	return c.CoreV1().PersistentVolumeClaims("mongodb").DeleteCollection(context.TODO(), metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", mongoReleaseName),
	})
}

func helmUninstall(releaseName string) error {
	cmd := exec.Command("helm", "uninstall", releaseName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", output)
	}
	return nil
}

func cleanup(c kubernetes.Clientset) error {
	log.Printf("Cleaning up resources")
	for i := 0; i < count; i++ {
		if tlsEnabled {
			err := helmUninstall(fmt.Sprintf("certs-%d", i))
			if err != nil {
				return err
			}
		}
		cleanMongoDBResource(c, fmt.Sprintf("mongo-%d", i))
	}

	if tlsEnabled {
		err := cleanupTLSSecrets(c)
		if err != nil {
			return err
		}
	}

	if deployYCSB && count == 1 {
		return helmUninstall("ycsb")
	}

	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	parseFlags()

	monitor, err := setup()
	if err != nil {
		log.Fatalf(err.Error())
	}
	monitor.Timeout = waitTime

	if cleanupResources {
		err := cleanup(*monitor.KubeClient)
		if err != nil {
			log.Fatalf("cleaning up resources: %s", err)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if deployOperatorAndOpsManager {
		if err := deployMongoDBOperatorAndOpsManager(*monitor.KubeClient, ctx); err != nil {
			log.Fatalf(err.Error())
		}
	} else {
		log.Print("skipping deploying operator and ops-manager")
	}

	err = wait.PollImmediate(30*time.Second, 10*time.Minute, areSecretsCreated(*monitor.KubeClient, "mongodb", "om-ops-manager-admin-key"))
	if err != nil {
		log.Fatal(err)
	}
	// Read user and password for ops manager
	omAdminKey, err := getSecretStringData(*monitor.KubeClient, "om-ops-manager-admin-key")
	if err != nil {
		log.Fatal(err)
	}

	if tlsEnabled {
		for i := 0; i < count; i++ {
			err = createTLSCerts(*monitor.KubeClient, fmt.Sprintf("mongo-%d", i), fmt.Sprintf("certs-%d", i))
			if err != nil {
				// we don't want to proceed if we had errors creating TLS certs
				log.Fatalf(err.Error())
			}
		}
	}

	// record the start time for deploying mongoDB replicasets
	monitor.StartTime = time.Now()

	for i := 0; i < count; i++ {
		err = deployMongoDB(ctx, fmt.Sprintf("mongo-%d", i), fmt.Sprintf("certs-%d", i), omAdminKey)
		if err != nil {
			// we don't want to proceed if we had errors deploying any of the mongodb replicasets
			log.Fatalf(err.Error())
		}
	}

	var wg sync.WaitGroup
	waitCh := make(chan struct{})

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
		// get required metrics from the operator over the timeframe needed by mongoDB replicasets to get in "ready" state.
		// TODO: make this configurable for multiple replicasets
		log.Printf("time taken by mongoDB replicaset to reach ready state: %.2f", getTimeDifferenceInSeconds(monitor.StartTime, monitor.EndTime))

		monitor.MonitorOperatorReconcileTime(ctx)
		monitor.MonitorOperatorResourceUsage(ctx)

		// we only want to run YCSB job when we are loadtesting agains one mongoDB replicaset as per spec
		if count == 1 && deployYCSB {
			// Added some sleep for ycsb to run sucessfully: https://jira.mongodb.org/browse/CLOUDP-76932
			time.Sleep(20 * time.Second)

			err := deployYCSBJob(ctx, *monitor.KubeClient, "mongo-0")
			if err != nil {
				log.Printf("error deploying/running ycsb: %v", err)
			} else {
				err = ycsb.ParseAndUploadYCSBPodLogs(ctx, *monitor.KubeClient, "mongodb", "ycsb-ycsb-job")
				if err != nil {
					log.Printf("error getting results from ycsb pod: %v", err)
				}
			}
		}

		log.Printf("loadtesting completed...")
	case <-time.After(waitTime):
		log.Printf("timedout waiting for response...")
	}
}
