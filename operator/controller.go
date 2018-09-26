package operator

import (
	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/operator/crd"
	"github.com/10gen/ops-manager-kubernetes/util"

	"fmt"

	"strings"

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

func (c *MongoDbController) prepareOmConnection(namespace, project, credentials string, log *zap.SugaredLogger) (om.OmConnection, *PodVars, error) {
	projectConfig, e := c.kubeHelper.readProjectConfig(namespace, project)
	if e != nil {
		return nil, nil, e
	}
	credsConfig, e := c.kubeHelper.readCredentials(namespace, credentials)
	if e != nil {
		return nil, nil, e
	}

	group, e := c.readOrCreateGroup(projectConfig, credsConfig, log)
	if e != nil {
		return nil, nil, e
	}

	omConnection := c.omConnectionFunc(projectConfig.BaseUrl, group.Id, credsConfig.User, credsConfig.PublicApiKey)

	agentKey, e := c.ensureAgentKeySecretExists(omConnection, namespace, group.AgentApiKey, log)

	if e != nil {
		return nil, nil, e
	}
	vars := &PodVars{
		BaseUrl:     projectConfig.BaseUrl,
		ProjectId:   group.Id,
		AgentApiKey: agentKey,
		User:        credsConfig.User,
	}
	return omConnection, vars, nil
}

// ensureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before)
// Returns the api key existing/generated
func (c *MongoDbController) ensureAgentKeySecretExists(omConnection om.OmConnection, nameSpace, agentKey string, log *zap.SugaredLogger) (string, error) {
	secretName := agentApiKeySecretName(omConnection.GroupId())
	log = log.With("secret", secretName)
	secret, err := c.kubeHelper.kubeApi.getSecret(nameSpace, secretName)
	if err != nil {
		if agentKey == "" {
			log.Info("Generating agent key as current group doesn't have it")

			agentKey, err = omConnection.GenerateAgentKey()
			if err != nil {
				return "", fmt.Errorf("Failed to generate agent key in OM: %s", err)
			}
			log.Info("Agent key was successfully generated")
		}

		if secret, err = c.kubeHelper.kubeApi.createSecret(nameSpace, buildSecret(secretName, nameSpace, agentKey)); err != nil {
			return "", fmt.Errorf("Failed to create Secret: %s", err)
		}
		log.Info("Group agent key is saved in Kubernetes Secret for later usage")
	}

	return strings.TrimSuffix(string(secret.Data[util.OmAgentApiKey]), "\n"), nil
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

func (c *MongoDbController) readOrCreateGroup(config *ProjectConfig, credentials *Credentials, log *zap.SugaredLogger) (*om.Group, error) {
	log = log.With("project", config.ProjectName)

	// we need to create a temporary connection object without group id
	conn := c.omConnectionFunc(config.BaseUrl, "", credentials.User, credentials.PublicApiKey)
	group, err := conn.ReadGroup(config.ProjectName)

	if err != nil {
		if (err.(*om.OmApiError)).ErrorCodeIn("GROUP_NAME_NOT_FOUND", "NOT_IN_GROUP") {
			group, err = tryCreateGroup(config, conn, log)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("Error reading group \"%s\" in Ops Manager: %s", config.ProjectName, err)
		}
	} else {
		log.Debugf("Group already exists")
	}

	// ensure the group has necessary tag
	for _, t := range group.Tags {
		if t == util.OmGroupExternallyManagedTag {
			return group, nil
		}
	}

	// So the group doesn't have necessary tag - let's fix it (this is a temporary solution and we must throw the
	// exception by 1.0)
	// return nil, fmt.Errorf("Group \"%s\" doesn't have the tag %s", config.ProjectName, OmGroupExternallyManagedTag)
	log.Infow("Seems group doesn't have necessary tag " + util.OmGroupExternallyManagedTag + " - updating it")

	groupWithTags := &om.Group{
		Name:  group.Name,
		OrgId: group.OrgId,
		Id:    group.Id,
		Tags:  append(group.Tags, util.OmGroupExternallyManagedTag),
	}
	g, err := conn.UpdateGroup(groupWithTags)
	if err != nil {
		log.Warnf("Failed to update tags for group: %s", err)
	} else {
		log.Infow("Group tags are fixed")
		group = g
	}

	return group, nil
}

func tryCreateGroup(config *ProjectConfig, conn om.OmConnection, log *zap.SugaredLogger) (*om.Group, error) {
	// Creating the group as it doesn't exist
	log.Infow("Creating the project as it doesn't exist", "orgId", config.OrgId)
	if config.OrgId == "" {
		log.Infof("Note that as the orgId is not specified the organization with name \"%s\" will be created "+
			"automatically by Ops Manager", config.ProjectName)
	}
	group := &om.Group{
		Name:  config.ProjectName,
		OrgId: config.OrgId,
		Tags:  []string{util.OmGroupExternallyManagedTag},
	}
	ans, err := conn.CreateGroup(group)

	if err != nil {
		apiError := err.(*om.OmApiError)
		if apiError.ErrorCodeIn("INVALID_ATTRIBUTE") && strings.Contains(apiError.Detail, "tags") {
			// Fallback logic: seems that OM version is < 4.0.2 (as it allows to edit group
			// tags only for GLOBAL_OWNER users), let's try to create group without tags
			group.Tags = []string{}
			ans, err = conn.CreateGroup(group)

			if err != nil {
				return nil, fmt.Errorf("Error creating group \"%s\" in Ops Manager: %s", group, err)
			}
			log.Infow("Created group without tags as current version of Ops Manager forbids tags modification")
		} else {
			return nil, fmt.Errorf("Error creating group \"%s\" in Ops Manager: %s", group, err)
		}
	}
	log.Infow("Project successfully created", "id", ans.Id)

	return ans, nil
}
