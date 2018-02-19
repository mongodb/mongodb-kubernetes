package main

import (
	"fmt"

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
	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAdd,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	}
	restClient := c.mongodbClientset.RESTClient()

	replicaSetWatcher := opkit.NewWatcher(mongodb.MongoDbReplicaSetResource, namespace, resourceHandlers, restClient)
	go replicaSetWatcher.Watch(&mongodb.MongoDbReplicaSet{}, stopCh)

	return nil
}

func (c *MongoDbController) onAdd(obj interface{}) {
	fmt.Println("onAddReplicaSet has been called")

	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	// TODO: here we are combining 2 APIs, Kubernetes and Mongo and we have confusing terms, like
	// the creation of a StatefulSet from a function that creates a replicaset? This is confusing and
	// this schema needs to be improved.
	deployment, err := c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Create(NewMongoDbReplicaSet(s))
	if err != nil {
		fmt.Printf("Error while creating the StatefulSet\n")
		fmt.Println(err)
		return
	}

	fmt.Printf("Created StatefulSet with %d replicas\n", *deployment.Spec.Replicas)
}

func (c *MongoDbController) onUpdate(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Updated MongoDbReplicaSet '%s' from %d to %d\n", newRes.Name, *oldRes.Spec.Members, *newRes.Spec.Members)

	if newRes.Namespace != oldRes.Namespace {
		panic("Two different namespaces?? whaaat?")
	}
	deployment, err := c.context.Clientset.AppsV1().StatefulSets(newRes.Namespace).Update(NewMongoDbReplicaSet(newRes))
	if err != nil {
		fmt.Printf("Error while creating the StatefulSet\n")
		fmt.Println(err)
		return
	}

	fmt.Printf("Updated StatefulSet '%s' with %d replicas\n", deployment.Name, *deployment.Spec.Replicas)
}

func (c *MongoDbController) onDelete(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Deleted MongoDbReplicaSet '%s' with Members=%d\n", s.Name, *s.Spec.Members)
}
