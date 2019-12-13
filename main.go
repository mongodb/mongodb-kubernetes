package main

import (
	"fmt"
	"os"
	"runtime"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
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

func main() {

	initializeEnvironment()

	// "WATCH_NAMESPACE" is taken from github.com/operator-framework/operator-sdk/pkg/k8sutil
	// copied the string value only to prevent importing the whole package
	// get watch namespace from environment variable
	namespace, namespaceSet := os.LookupEnv("WATCH_NAMESPACE")

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
	if err := mdbv1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr); err != nil {
		log.Fatal(err)
	}

	log.Info("Starting the Cmd.")

	// Start the Manager
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}

func initializeEnvironment() {
	env := os.Getenv(util.OmOperatorEnv)
	configuredEnv := env
	if !validateEnv(env) {
		env = operatorEnvironments[0]
	}

	initLogger(env)

	if configuredEnv != env {
		log.Infof("Configured environment %s, not recognized. Must be one of %v", configuredEnv, operatorEnvironments)
		log.Infof("Using default environment, %s, instead", operatorEnvironments[0])
	}

	initEnvVariables(env)

	log.Infof("Operator environment: %s", env)
	log.Infof("Operator version: %s", util.OperatorVersion)
	log.Infof("Go Version: %s", runtime.Version())
	log.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	util.PrintEnvVars()
}

// initEnvVariables is the central place in application to initialize default configuration for the application (using
// env variables). Having the central place to manage defaults increases manageability and transparency of the application
// Method initializes variables only in case they are not specified already.
func initEnvVariables(env string) {
	util.EnsureEnvVar(util.BackupDisableWaitSecondsEnv, util.DefaultBackupDisableWaitSeconds)
	util.EnsureEnvVar(util.BackupDisableWaitRetriesEnv, util.DefaultBackupDisableWaitRetries)
}

func validateEnv(env string) bool {
	return util.ContainsString(operatorEnvironments[:], env)
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
