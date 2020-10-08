package agents

import (
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretGetCreator interface {
	secret.Getter
	secret.Creator
}

// ensureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before)
// Returns the api key existing/generated
func EnsureAgentKeySecretExists(secretGetCreator SecretGetCreator, agentKeyGenerator om.AgentKeyGenerator, nameSpace, agentKey, projectId string, log *zap.SugaredLogger) error {
	secretName := agentApiKeySecretName(projectId)
	log = log.With("secret", secretName)
	_, err := secretGetCreator.GetSecret(kube.ObjectKey(nameSpace, secretName))
	if err != nil {
		if agentKey == "" {
			log.Info("Generating agent key as current project doesn't have it")

			agentKey, err = agentKeyGenerator.GenerateAgentKey()
			if err != nil {
				return fmt.Errorf("Failed to generate agent key in OM: %s", err)
			}
			log.Info("Agent key was successfully generated")
		}

		// todo pass a real owner in a next PR
		if err = createAgentKeySecret(secretGetCreator, kube.ObjectKey(nameSpace, secretName), agentKey, nil); err != nil {
			if apiErrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("Failed to create Secret: %s", err)
		}
		log.Infof("Project agent key is saved in Kubernetes Secret for later usage")
		return nil
	}

	return nil
}

func createAgentKeySecret(secretCreator secret.Creator, objectKey client.ObjectKey, agentKey string, owner v1.CustomResourceReadWriter) error {
	agentKeySecret := secret.Builder().
		SetField(util.OmAgentApiKey, agentKey).
		SetOwnerReferences(kube.BaseOwnerReference(owner)).
		SetName(objectKey.Name).
		SetNamespace(objectKey.Namespace).
		Build()
	return secretCreator.CreateSecret(agentKeySecret)
}

// agentApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func agentApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}
