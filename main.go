package main

import (
	"fmt"
	"os"
	"runtime"

	apis "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"go.uber.org/zap"
)

var log *zap.SugaredLogger

func main() {

	initializeEnvironment()

	// get watch namespace from environment variable
	namespace, namespaceSet := os.LookupEnv(k8sutil.WatchNamespaceEnvVar)

	// if namespace is set to the wildcard then use the empty string to represent all namespaces
	if namespace == "*" || !namespaceSet {
		log.Info("Monitoring all namespaces")
		namespace = ""
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

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr); err != nil {
		log.Fatal(err)
	}

	log.Info("Starting the Cmd.")

	// Start the Manager
	log.Fatal(mgr.Start(signals.SetupSignalHandler()))
}

func initializeEnvironment() {
	env := os.Getenv(util.OmOperatorEnv)

	validateEnv(env)

	initLogger(env)

	initEnvVariables(env)

	log.Infof("Operator environment: %s", env)
	log.Infof("Go Version: %s", runtime.Version())
	log.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	util.PrintEnvVars()
}

// initEnvVariables is the central place in application to initialize default configuration for the application (using
// env variables). Having the central place to manage defaults increases manageability and transparency of the application
// Method initializes variables only in case they are not specified already.
func initEnvVariables(env string) {
	switch env {
	case "prod":
		util.EnsureEnvVar(util.PodWaitSecondsEnv, util.DefaultPodWaitSecondsProd)
		util.EnsureEnvVar(util.PodWaitRetriesEnv, util.DefaultPodWaitRetriesProd)
	case "dev", "local":
		util.EnsureEnvVar(util.PodWaitSecondsEnv, util.DefaultPodWaitSecondsDev)
		util.EnsureEnvVar(util.PodWaitRetriesEnv, util.DefaultPodWaitRetriesDev)
	}
	util.EnsureEnvVar(util.BackupDisableWaitSecondsEnv, util.DefaultBackupDisableWaitSeconds)
	util.EnsureEnvVar(util.BackupDisableWaitRetriesEnv, util.DefaultBackupDisableWaitRetries)
}

func validateEnv(env string) {
	switch env {
	case "prod", "dev", "local":
		return
	}
	zap.S().Error("Wrong environment specified", "env", env)
	os.Exit(1)
}

func initLogger(env string) {
	var logger *zap.Logger
	var e error

	switch env {
	case "prod":
		logger, e = zap.NewProduction()
	case "dev", "local":
		logger, e = zap.NewDevelopment()
	}

	if e != nil {
		fmt.Println("Failed to create logger, will use the default one")
		fmt.Println(e)
	}
	zap.ReplaceGlobals(logger)
	log = zap.S()
}
