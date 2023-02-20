package main

import (
	"flag"
	"fmt"
	"os"
	localruntime "runtime"
	"strconv"
	"strings"

	apiv1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/controllers"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	runtime_cluster "sigs.k8s.io/controller-runtime/pkg/cluster"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

var (
	log *zap.SugaredLogger

	// List of allowed operator environments. The first element of this list is
	// considered the default one.
	operatorEnvironments = []string{util.OperatorEnvironmentDev, util.OperatorEnvironmentLocal, util.OperatorEnvironmentProd}

	scheme = runtime.NewScheme()
)

const (
	mdbWebHookPortEnvName = "MDB_WEBHOOK_PORT"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

// commandLineFlags struct holds the command line arguments passed to the operator deployment
type commandLineFlags struct {
	crdsToWatch    string
	memberClusters []string
}

// crdsToWatch is a custom Value implementation which can be
// used to receive command line arguments
type crdsToWatch []string

func (c *crdsToWatch) Set(value string) error {
	*c = append(*c, value)
	return nil
}

func (c *crdsToWatch) String() string {
	return strings.Join(*c, ",")
}

// memberClusterNames gets the name of all the member clusters where
// the operator should deploy the MongoDB Replicaset.
type memberClusterNames []string

func (m *memberClusterNames) String() string {
	return fmt.Sprintln(*m)
}

func (m *memberClusterNames) Set(s string) error {
	*m = strings.Split(s, ",")
	return nil
}

// parseCommandLineArgs parses the command line arguments passed in the operator deployment specs
func parseCommandLineArgs() commandLineFlags {
	crds := crdsToWatch{}
	clusterNames := memberClusterNames{}

	flag.Var(&crds, "watch-resource", "A Watch Resource specifies if the Operator should watch the given resource")
	flag.Var(&clusterNames, "cluster-names", "The list of cluster names where the operator should deploy the mongoDB ReplicaSet")
	flag.Parse()

	return commandLineFlags{
		crdsToWatch:    crds.String(),
		memberClusters: clusterNames,
	}
}

func main() {
	initializeEnvironment()

	// Get a config to talk to the apiserver
	cfg := ctrl.GetConfigOrDie()

	managerOptions := ctrl.Options{
		Scheme: scheme,
	}

	// Namespace where the operator is installed
	currentNamespace := env.ReadOrPanic(util.CurrentNamespace)

	namespacesToWatch := operator.GetWatchedNamespace()
	if len(namespacesToWatch) == 1 {
		// This will be the name of 1 namespace to watch, or the empty string
		// for an operator that watches the whole cluster
		managerOptions.Namespace = namespacesToWatch[0]
	} else {
		if !stringutil.Contains(namespacesToWatch, currentNamespace) {
			namespacesToWatch = append(namespacesToWatch, currentNamespace)
		}
		// In multi-namespace scenarios, the namespace where the Operator
		// resides needs to be part of the Cache as well.
		managerOptions.NewCache = cache.MultiNamespacedCacheBuilder(namespacesToWatch)
	}

	if isInLocalMode() {
		managerOptions.MetricsBindAddress = "127.0.0.1:8180"
		managerOptions.HealthProbeBindAddress = "127.0.0.1:8181"
	}

	mgr, err := ctrl.NewManager(cfg, managerOptions)
	if err != nil {
		log.Fatal(err)
	}
	log.Info("Registering Components.")

	commandLineFlags := parseCommandLineArgs()
	crdsToWatch := commandLineFlags.crdsToWatch
	setupWebhook(mgr, cfg, log, multicluster.IsMultiClusterMode(crdsToWatch))

	// Setup Scheme for all resources
	if err := apiv1.AddToScheme(scheme); err != nil {
		log.Fatal(err)
	}

	// memberClusterObjectsMap is a map of clusterName -> clusterObject
	memberClusterObjectsMap := make(map[string]runtime_cluster.Cluster)

	if multicluster.IsMultiClusterMode(crdsToWatch) {
		memberClustersNames := commandLineFlags.memberClusters
		log.Infof("Watching Member clusters: %s", memberClustersNames)
		// get cluster clients for the member clusters
		memberClusterClients, err := multicluster.CreateMemberClusterClients(memberClustersNames)
		if err != nil {
			log.Fatal(err)
		}

		kubeConfigFile, err := multicluster.NewKubeConfigFile()
		if err != nil {
			log.Fatalf("failed to open kubeconfig file: %s, err: %s", multicluster.GetKubeConfigPath(), err)
		}

		kubeConfig, err := kubeConfigFile.LoadKubeConfigFile()
		if err != nil {
			log.Fatal("failed reading KubeConfig file: %s", err)
		}

		// Add the cluster object to the manager corresponding to each member clusters.
		for k, v := range memberClusterClients {
			var cluster runtime_cluster.Cluster
			// if length of namespaces is 1(one particular namespace or * namespace) we can use the namespace in options
			// but if we are watching a subset of namespaces we need to initialize the cache with specific namespaces only
			if len(namespacesToWatch) == 1 {
				cluster, err = runtime_cluster.New(v, func(options *runtime_cluster.Options) {
					options.Namespace = kubeConfig.GetMemberClusterNamespace()
				})
				if err != nil {
					// don't panic here but rather log the error, for example, error might happen when one of the cluster is
					// unreachable, we would still like the operator to continue reconciliation on the other clusters.
					log.Errorf("Failed to initialize client for cluster: %s, err: %s", k, err)
					continue
				}
			} else if len(namespacesToWatch) > 1 {
				log.Infof("Building member cluster cache for multiple namespaces: %v", namespacesToWatch)
				cluster, err = runtime_cluster.New(v, func(options *runtime_cluster.Options) {
					options.NewCache = cache.MultiNamespacedCacheBuilder(namespacesToWatch)
				})
				if err != nil {
					log.Errorf("Failed to initialize client for cluster: %s, err: %s", k, err)
					continue
				}
			}

			log.Infof("Adding cluster %s to cluster map.", k)
			memberClusterObjectsMap[k] = cluster
			if err = mgr.Add(cluster); err != nil {
				log.Fatal(err)
			}
		}
	}

	// Setup all Controllers
	var registeredCRDs []string
	if registeredCRDs, err = controllers.AddToManager(mgr, crdsToWatch, memberClusterObjectsMap); err != nil {
		log.Fatal(err)
	}

	for _, r := range registeredCRDs {
		log.Infof("Registered CRD: %s", r)
	}

	log.Info("Starting the Cmd.")

	// Start the Manager
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}

func isInLocalMode() bool {
	return operatorEnvironments[1] == env.ReadOrPanic(util.OmOperatorEnv)
}

// setupWebhook sets up the validation webhook for MongoDB resources in order
// to give people early warning when their MongoDB resources are wrong.
func setupWebhook(mgr manager.Manager, cfg *rest.Config, log *zap.SugaredLogger, multiClusterMode bool) {
	// set webhook port — 1993 is chosen as Ben's birthday
	webhookPort := env.ReadIntOrDefault(mdbWebHookPortEnvName, 1993)
	mgr.GetWebhookServer().Port = webhookPort
	if isInLocalMode() {
		mgr.GetWebhookServer().Host = "127.0.0.1"
	}

	// this is the default directory on Linux but setting it explicitly helps
	// with cross-platform compatibility, specifically local development on MacOS
	certDir := "/tmp/k8s-webhook-server/serving-certs/"
	mgr.GetWebhookServer().CertDir = certDir

	// create a kubernetes client that the webhook server can use. We can't reuse
	// the one from the manager as it is not initialised yet.
	webhookClient, err := client.New(cfg, client.Options{})
	if err != nil {
		panic(err)
	}

	// webhookServiceLocation is the name and namespace of the webhook service
	// that will be created.
	webhookServiceLocation := types.NamespacedName{
		Name:      "operator-webhook",
		Namespace: env.ReadOrPanic(util.CurrentNamespace),
	}
	if err := webhook.Setup(webhookClient, webhookServiceLocation, certDir, webhookPort, multiClusterMode); err != nil {
		log.Warnw("could not set up webhook", "error", err)
	}
	log.Info("setup webhook successfully")
}

func initializeEnvironment() {
	omOperatorEnv := os.Getenv(util.OmOperatorEnv)
	configuredEnv := omOperatorEnv
	if !validateEnv(omOperatorEnv) {
		omOperatorEnv = operatorEnvironments[0]
	}

	initLogger(omOperatorEnv)

	if configuredEnv != omOperatorEnv {
		log.Infof("Configured environment %s, not recognized. Must be one of %v", configuredEnv, operatorEnvironments)
		log.Infof("Using default environment, %s, instead", operatorEnvironments[0])
	}

	initEnvVariables()

	log.Infof("Operator environment: %s", omOperatorEnv)
	log.Infof("Operator version: %s", util.OperatorVersion)
	log.Infof("Go Version: %s", localruntime.Version())
	log.Infof("Go OS/Arch: %s/%s", localruntime.GOOS, localruntime.GOARCH)

	printableEnvPrefixes := []string{
		"BACKUP_WAIT_",
		"POD_WAIT_",
		"OPERATOR_ENV",
		"WATCH_NAMESPACE",
		"MANAGED_SECURITY_CONTEXT",
		"IMAGE_PULL_SECRETS",
		"MONGODB_ENTERPRISE_",
		"OPS_MANAGER_",
		"KUBERNETES_",
		"AGENT_IMAGE",
		"MONGODB_",
		"INIT_",
	}

	// Only env variables with one of these prefixes will be printed
	env.PrintWithPrefix(printableEnvPrefixes)
}

// initEnvVariables is the central place in application to initialize default configuration for the application (using
// env variables). Having the central place to manage defaults increases manageability and transparency of the application
// Method initializes variables only in case they are not specified already.
func initEnvVariables() {
	env.EnsureVar(util.BackupDisableWaitSecondsEnv, util.DefaultBackupDisableWaitSeconds)
	env.EnsureVar(util.BackupDisableWaitRetriesEnv, util.DefaultBackupDisableWaitRetries)
	env.EnsureVar(util.OpsManagerMonitorAppDB, strconv.FormatBool(util.OpsManagerMonitorAppDBDefault))
}

func validateEnv(env string) bool {
	return stringutil.Contains(operatorEnvironments[:], env)
}

func initLogger(env string) {
	var logger *zap.Logger
	var e error

	switch env {
	case "prod":
		logger, e = zap.NewProduction()
	case "dev", "local":
		// Overriding the default stacktrace behavior - have them only for errors but not for warnings
		logger, e = zap.NewDevelopment(zap.AddStacktrace(zap.ErrorLevel))
	}

	if e != nil {
		fmt.Println("Failed to create logger, will use the default one")
		fmt.Println(e)
	}
	zap.ReplaceGlobals(logger)
	log = zap.S()
}
