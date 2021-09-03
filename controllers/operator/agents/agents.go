package agents

import (
	"errors"
	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
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

// ApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func ApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

func WaitForRsAgentsToRegister(set appsv1.StatefulSet, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	return WaitForRsAgentsToRegisterReplicasSpecified(set, 0, clusterName, omConnection, log)
}

// WaitForRsAgentsToRegister waits until all the agents associated with the given StatefulSet have registered with Ops Manager.
func WaitForRsAgentsToRegisterReplicasSpecified(set appsv1.StatefulSet, members int, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	hostnames, _ := dns.GetDnsForStatefulSetReplicasSpecified(set, clusterName, members)
	log = log.With("statefulset", set.Name)

	if !waitUntilRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register or the Operator is using the wrong host names for the pods. " +
			"Make sure the 'spec.clusterDomain' is set if it's different from the default Kubernetes cluster " +
			"name ('cluster.local') ")
	}
	return nil
}

// WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster waits for the specified agents to registry with Ops Manager.
func WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster(omConnection om.Connection, hostnames []string, log *zap.SugaredLogger) error {
	if !waitUntilRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register or the Operator is using the wrong host names for the pods. " +
			"Make sure the 'spec.clusterDomain' is set if it's different from the default Kubernetes cluster " +
			"name ('cluster.local') ")
	}
	return nil
}

// waitUntilRegistered waits until all agents with 'agentHostnames' are registered in OM. Note, that wait
// happens after retrial - this allows to skip waiting in case agents are already registered
func waitUntilRegistered(omConnection om.Connection, log *zap.SugaredLogger, agentHostnames ...string) bool {
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	// environment variables are used only for tests
	waitSeconds := env.ReadIntOrDefault(util.PodWaitSecondsEnv, 3)
	retrials := env.ReadIntOrDefault(util.PodWaitRetriesEnv, 5)

	agentsCheckFunc := func() (string, bool) {
		registeredCount := 0
		found, err := om.TraversePages(
			omConnection.ReadAutomationAgents,
			func(aa interface{}) bool {
				automationAgent := aa.(om.AgentStatus)

				for _, hostname := range agentHostnames {
					if automationAgent.IsRegistered(hostname, log) {
						registeredCount++
						if registeredCount == len(agentHostnames) {
							return true
						}
					}
				}
				return false
			},
		)

		if err != nil {
			log.Errorw("Received error when reading automation agent pages", "err", err)
		}

		var msg string
		if registeredCount == 0 {
			msg = fmt.Sprintf("None of %d agents has registered with OM", len(agentHostnames))
		} else {
			msg = fmt.Sprintf("Only %d of %d agents have registered with OM", registeredCount, len(agentHostnames))
		}
		return msg, found
	}

	return util.DoAndRetry(agentsCheckFunc, log, retrials, waitSeconds)
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
