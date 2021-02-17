package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/controllers"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/webhook"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

var (
	log *zap.SugaredLogger

	// List of allowed operator environments. The first element of this list is
	// considered the default one.
	operatorEnvironments = [...]string{"dev", "local", "prod"}
)

const (
	mdbWebHookPortEnvName = "MDB_WEBHOOK_PORT"
)

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

// getCrdsToWatchStr returns a comma separated list of strings which represent which CRDs should be watched
func getCrdsToWatchStr() string {
	crds := crdsToWatch{}
	flag.Var(&crds, "watch-resource", "A Watch Resource specifies if the Operator should watch the given resource")
	flag.Parse()
	return crds.String()
}

func main() {

	initializeEnvironment()

	// get watch namespace from environment variable
	namespace, nsSpecified := os.LookupEnv(util.WatchNamespace)

	// if the watch namespace is not specified - we assume the Operator is watching the current namespace
	if !nsSpecified {
		namespace = env.ReadOrPanic(util.CurrentNamespace)
	}

	// if namespace is set to the wildcard then use the empty string to represent all namespaces
	if namespace == "*" {
		log.Info("Watching all namespaces")
		namespace = ""
	}

	// The case when the Operator is watching only a single namespace different from the current
	if env.ReadOrPanic(util.CurrentNamespace) != namespace {
		log.Infof("Watching namespace %s", namespace)
	}

	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{Namespace: namespace})
	if err != nil {
		log.Fatal(err)
	}

	log.Info("Registering Components.")

	setupWebhook(mgr, cfg, log)

	// Setup Scheme for all resources
	if err := mdbv1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// Setup all Controllers
	var registeredCRDs []string
	if registeredCRDs, err = controllers.AddToManager(mgr, getCrdsToWatchStr()); err != nil {
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

// setupWebhook sets up the validation webhook for MongoDB resources in order
// to give people early warning when their MongoDB resources are wrong.
func setupWebhook(mgr manager.Manager, cfg *rest.Config, log *zap.SugaredLogger) {
	// set webhook port — 1993 is chosen as Ben's birthday
	webhookPort := env.ReadIntOrDefault(mdbWebHookPortEnvName, 1993)
	mgr.GetWebhookServer().Port = webhookPort

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
	if err := webhook.Setup(webhookClient, webhookServiceLocation, certDir, webhookPort); err != nil {
		log.Warnw("could not set up webhook", "error", err)
	}
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
	log.Infof("Go Version: %s", runtime.Version())
	log.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)

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
