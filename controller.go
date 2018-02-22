package main

import (
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	mongodbscheme "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/scheme"
	mongodbclient "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/typed/mongodb.com/v1alpha1"
	opkit "github.com/rook/operator-kit"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
)

const LabelApp = "om-controller"
const LabelController = "om-controller"

type MongoDbController struct {
	context          *opkit.Context
	mongodbClientset mongodbclient.MongodbV1alpha1Interface
}

func newMongoDbController(context *opkit.Context, mongodbClientset mongodbclient.MongodbV1alpha1Interface) *MongoDbController {
	mongodbscheme.AddToScheme(scheme.Scheme)

	return &MongoDbController{
		context:          context,
		mongodbClientset: mongodbClientset,
	}
}

func (c *MongoDbController) StartWatch(namespace string, stopCh chan struct{}) error {
	err := c.startWatchReplicaSet(namespace, stopCh)
	if err != nil {
		return err
	}
	err = c.startWatchStandalone(namespace, stopCh)
	if err != nil {
		return err
	}

	return nil
}

func (c *MongoDbController) startWatchReplicaSet(namespace string, stopCh chan struct{}) error {
	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAddReplicaSet,
		UpdateFunc: c.onUpdateReplicaSet,
		DeleteFunc: c.onDeleteReplicaSet,
	}
	restClient := c.mongodbClientset.RESTClient()

	replicaSetWatcher := opkit.NewWatcher(mongodb.MongoDbReplicaSetResource, namespace, resourceHandlers, restClient)
	go replicaSetWatcher.Watch(&mongodb.MongoDbReplicaSet{}, stopCh)

	return nil
}

func (c *MongoDbController) startWatchStandalone(namespace string, stopCh chan struct{}) error {
	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAddStandalone,
		UpdateFunc: c.onUpdateStandalone,
		DeleteFunc: c.onDeleteStandalone,
	}
	restClient := c.mongodbClientset.RESTClient()

	replicaSetWatcher := opkit.NewWatcher(mongodb.MongoDbStandaloneResource, namespace, resourceHandlers, restClient)
	go replicaSetWatcher.Watch(&mongodb.MongoDbStandalone{}, stopCh)

	return nil
}
