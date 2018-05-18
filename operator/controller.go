package operator

import (
	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/operator/crd"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	mongodbscheme "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/scheme"
	mongodbclient "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/typed/mongodb.com/v1alpha1"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	coreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
)

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
	err = c.startWatchShardedCluster(namespace, stopCh)
	if err != nil {
		return err
	}

	return nil
}

func (c *MongoDbController) getOmConnection(namespace string, omConfigMapName string) (*om.OmConnection, error) {
	if omConfigMapName == "" {
		return nil, errors.New("ops_manager_config spec parameter must be specified!")
	}
	data, e := c.kubeHelper.readConfigMap(namespace, omConfigMapName)
	if e != nil {
		return nil, e
	}

	return &om.OmConnection{
			GroupId:      data[OmGroupId],
			User:         data[OmUserName],
			PublicApiKey: data[OmPublicKey],
			BaseUrl:      data[OmBaseUrl],
		},
		nil

}

func (c *MongoDbController) SecretsApi(namespace string) coreV1.SecretInterface {
	return c.context.Clientset.CoreV1().Secrets(namespace)
}

// EnsureAgentKeySecretExists checks if the Secret with specified name (equal to group id) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key
func (c *MongoDbController) EnsureAgentKeySecretExists(omConnection *om.OmConnection, nameSpace string) (string, error) {
	secretName := omConnection.GroupId
	log := zap.S().With("secret", secretName)
	_, err := c.SecretsApi(nameSpace).Get(secretName, v1.GetOptions{})
	if err != nil {
		log.Info("Failed to find the Secret, generating agent key to create the new one")

		key, err := omConnection.GenerateAgentKey()
		if err != nil {
			log.Error("Failed to generate agent Key")
			return "", err
		}
		if _, err := c.SecretsApi(nameSpace).Create(buildSecret(secretName, nameSpace, key)); err != nil {
			log.Error("Failed to create Secret")
			return "", err
		}
		log.Info("New Ops Manager agent Key is generated and saved in Kubernetes Secret for later usage")
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

func (c *MongoDbController) startWatchShardedCluster(namespace string, stopCh chan struct{}) error {
	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAddShardedCluster,
		UpdateFunc: c.onUpdateShardedCluster,
		DeleteFunc: c.onDeleteShardedCluster,
	}
	restClient := c.mongodbClientset.RESTClient()

	replicaSetWatcher := crd.NewWatcher(mongodb.MongoDbShardedClusterResource, namespace, resourceHandlers, restClient)
	go replicaSetWatcher.Watch(&mongodb.MongoDbShardedCluster{}, stopCh)

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
