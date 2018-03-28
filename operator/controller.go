package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/operator/crd"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	mongodbscheme "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/scheme"
	mongodbclient "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/typed/mongodb.com/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	appsV1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	coreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
)

const LabelApp = "om-controller"
const LabelController = "om-controller"

type MongoDbController struct {
	context          *crd.Context
	mongodbClientset mongodbclient.MongodbV1alpha1Interface
	kubeHelper       KubeHelper
}

func NewMongoDbController(context *crd.Context, mongodbClientset mongodbclient.MongodbV1alpha1Interface) *MongoDbController {
	mongodbscheme.AddToScheme(scheme.Scheme)

	return &MongoDbController{
		context:          context,
		mongodbClientset: mongodbClientset,
		kubeHelper:       KubeHelper{context.Clientset},
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

func (c *MongoDbController) StatefulSetApi(namespace string) appsV1.StatefulSetInterface {
	return c.context.Clientset.AppsV1().StatefulSets(namespace)
}

func (c *MongoDbController) SecretsApi(namespace string) coreV1.SecretInterface {
	return c.context.Clientset.CoreV1().Secrets(namespace)
}

// EnsureAgentKeySecretExists checks if the Secret with specified name (equal to group id) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key
func (c *MongoDbController) EnsureAgentKeySecretExists(nameSpace string, omConnection *om.OmConnection) (string, error) {
	secretName := omConnection.GroupId
	_, err := c.SecretsApi(nameSpace).Get(secretName, v1.GetOptions{})
	if err != nil {
		fmt.Printf("Error finding Secret with name %s, generating agent key to create the new one\n", secretName)

		key, err := omConnection.GenerateAgentKey()
		if err != nil {
			fmt.Println("Failed to generate agent Key")
			return "", err
		}
		if _, err := c.SecretsApi(nameSpace).Create(buildSecret(secretName, nameSpace, key)); err != nil {
			fmt.Printf("Failed to create Secret with name %s\n", secretName)
			return "", err
		}
		fmt.Printf("New Ops Manager agent Key for group %s generated and saved in Kubernetes for later usage\n", secretName)
	}
	return secretName, nil
}

func (c *MongoDbController) startWatchReplicaSet(namespace string, stopCh chan struct{}) error {
	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAddReplicaSet,
		UpdateFunc: c.onUpdateReplicaSet,
		DeleteFunc: c.onDeleteReplicaSet,
	}
	restClient := c.mongodbClientset.RESTClient()

	replicaSetWatcher := crd.NewWatcher(mongodb.MongoDbReplicaSetResource, namespace, resourceHandlers, restClient)
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

	replicaSetWatcher := crd.NewWatcher(mongodb.MongoDbStandaloneResource, namespace, resourceHandlers, restClient)
	go replicaSetWatcher.Watch(&mongodb.MongoDbStandalone{}, stopCh)

	return nil
}
