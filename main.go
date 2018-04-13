package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flag"

	"github.com/10gen/ops-manager-kubernetes/operator"
	"github.com/10gen/ops-manager-kubernetes/operator/crd"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	mongodbclient "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/typed/mongodb.com/v1alpha1"
	"k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var log *zap.SugaredLogger

func main() {

	initializeEnvironment()

	context, mongodbClientset, err := createContext()
	if err != nil {
		log.Error("Failed to create context: ", err)
		os.Exit(1)
	}

	// Create and wait for CRD resources
	resources := []crd.CustomResource{
		mongodb.MongoDbReplicaSetResource,
		mongodb.MongoDbStandaloneResource,
		mongodb.MongoDbShardedClusterResource}

	resourceNames := make([]string, len(resources))
	for i, r := range resources {
		resourceNames[i] = r.Name
	}
	log.Infow("Ensuring the Custom Resource Definitions exist", "crds", resourceNames)
	err = crd.BuildCustomResources(*context, resources)
	if err != nil {
		log.Error("failed to create custom resource: ", err)
		os.Exit(1)
	}

	// create signals to stop watching the resources
	signalChan := make(chan os.Signal, 1)
	stopChan := make(chan struct{})
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	// start watching the sample resources
	log.Info("Starting watching resources for CRDs just created")
	controller := operator.NewMongoDbController(context, mongodbClientset)
	controller.StartWatch(v1.NamespaceAll, stopChan)

	for {
		select {
		case <-signalChan:
			log.Warn("shutdown signal received, exiting...")
			close(stopChan)
			return
		}
	}
}

func initializeEnvironment() {
	envPtr := flag.String("env", "prod", "Name of environment used. Must be one of [\"prod\", \"dev\", \"local\"]")

	flag.Parse()

	env := *envPtr

	validateEnv(env)

	initLogger(env)

	initConfiguration(env)

	log.Info("Operator environment: ", env)
}

func validateEnv(env string) {
	switch env {
	case "prod", "dev", "local":
		return
	}
	zap.S().Error("Wrong environment specified", "env", env)
	flag.Usage()
	os.Exit(1)
}

func initConfiguration(env string) {
	switch env {
	case "prod":
		viper.SetConfigFile("config/prod.properties")
	case "dev":
		viper.SetConfigFile("config/dev.properties")
	case "local":
		viper.SetConfigFile("config/local.properties")
	}

	viper.SetConfigName("config")
	viper.SetConfigType("props")
	err := viper.ReadInConfig()
	if err != nil {
		log.Error("Failed to read configuration file", "config file", viper.ConfigFileUsed())
		os.Exit(1)
	}
	viper.Set(operator.Mode, env)
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

func createContext() (*crd.Context, mongodbclient.MongodbV1alpha1Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get k8s config. %+v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get k8s client. %+v", err)
	}

	apiExtClientset, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create k8s API extension clientset. %+v", err)
	}

	mongodbClientset, err := mongodbclient.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create clientset. %+v", err)
	}

	context := &crd.Context{
		Clientset:             clientset,
		APIExtensionClientset: apiExtClientset,
		Interval:              500 * time.Millisecond,
		Timeout:               60 * time.Second,
	}
	return context, mongodbClientset, nil

}
