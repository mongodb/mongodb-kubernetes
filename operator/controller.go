package operator

import (
	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/operator/crd"

	"errors"
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	mongodbscheme "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/scheme"
	mongodbclient "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/typed/mongodb.com/v1"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
)

type MongoDbController struct {
	mongodbClientset mongodbclient.MongodbV1Interface
	kubeHelper       KubeHelper
	omConnectionFunc func(baseUrl, groupId, user, publicApiKey string) om.OmConnection
	// the connection object created by the function above. Production code hardly needs it but it's convenient for testing
	omConnection om.OmConnection
}

// NewMongoDbController creates the controller class that reacts on events for mongodb resources
// Passing "KubeApi" and "omConnectionFunc" parameters allows inject the custom behavior and is convenient for testing
func NewMongoDbController(kubeApi KubeApi, mongodbClientset mongodbclient.MongodbV1Interface,
	omConnectionFunc func(baseUrl, groupId, user, publicApiKey string) om.OmConnection) *MongoDbController {
	mongodbscheme.AddToScheme(scheme.Scheme)

	return &MongoDbController{
		mongodbClientset: mongodbClientset,
		kubeHelper:       KubeHelper{kubeApi: kubeApi},
		omConnectionFunc: omConnectionFunc,
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

func (c *MongoDbController) createOmConnection(namespace, project, credentials string) (om.OmConnection, error) {
	projectConfig, e := c.kubeHelper.readProjectConfig(namespace, project)
	if e != nil {
		return nil, e
	}
	credsConfig, e := c.kubeHelper.readCredentials(namespace, credentials)
	if e != nil {
		return nil, e
	}

	c.omConnection = c.omConnectionFunc(projectConfig.BaseUrl, projectConfig.ProjectId,
		credsConfig.User, credsConfig.PublicApiKey)
	return c.omConnection, nil
}

func (c *MongoDbController) buildPodVars(namespace, project, credentials, agent string) (*PodVars, error) {
	projectConfig, e := c.kubeHelper.readProjectConfig(namespace, project)
	if e != nil {
		return nil, e
	}

	credsConfig, e := c.kubeHelper.readCredentials(namespace, credentials)
	if e != nil {
		return nil, e
	}

	agentSecret, e := c.kubeHelper.readAgentApiKeyForProject(namespace, agent)
	if e != nil {
		return nil, e
	}

	return &PodVars{
		BaseUrl:     projectConfig.BaseUrl,
		ProjectId:   projectConfig.ProjectId,
		AgentApiKey: agentSecret,
		User:        credsConfig.User,
	}, nil
}

// ensureAgentKeySecretExists checks if the Secret with specified name (equal to group id) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key
func (c *MongoDbController) ensureAgentKeySecretExists(omConnection om.OmConnection, nameSpace string, log *zap.SugaredLogger) (string, error) {
	secretName := agentApiKeySecretName(omConnection.GroupId())
	log = log.With("secret", secretName)
	_, err := c.kubeHelper.kubeApi.getSecret(nameSpace, secretName)
	if err != nil {
		log.Info("Failed to find the Secret, generating agent key to create the new one")

		key, err := omConnection.GenerateAgentKey()
		if err != nil {
			return "", errors.New(fmt.Sprintf("Failed to generate agent Key in OM: %s", err))
		}
		if _, err := c.kubeHelper.kubeApi.createSecret(nameSpace, buildSecret(secretName, nameSpace, key)); err != nil {
			return "", errors.New(fmt.Sprintf("Failed to create Secret: %s", err))
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
