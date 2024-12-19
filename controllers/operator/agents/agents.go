package agents

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

type SecretGetCreator interface {
	secret.Getter
	secret.Creator
}

type retryParams struct {
	waitSeconds int
	retrials    int
}

// EnsureAgentKeySecretExists checks if the Secret with specified name (<groupId>-group-secret) exists, otherwise tries to
// generate agent key using OM public API and create Secret containing this key. Generation of a key is expected to be
// a rare operation as the group creation api generates agent key already (so the only possible situation is when the group
// was created externally and agent key wasn't generated before).
// Returns agent key that was either generated or reused from parameter agentKey.
// We need to return the key, because in case it was generated here it has to be passed back on an agentKey argument when we're executing the
// function over multiple clusters.
func EnsureAgentKeySecretExists(ctx context.Context, secretClient secrets.SecretClient, agentKeyGenerator om.AgentKeyGenerator, namespace, agentKey, projectId, basePath string, log *zap.SugaredLogger) (string, error) {
	secretName := ApiKeySecretName(projectId)
	log = log.With("secret", secretName)
	agentKeySecret, err := secretClient.ReadSecret(ctx, kube.ObjectKey(namespace, secretName), basePath)
	if err != nil {
		if !secrets.SecretNotExist(err) {
			return "", xerrors.Errorf("error reading agent key secret: %w", err)
		}

		if agentKey == "" {
			log.Info("Generating agent key as current project doesn't have it")

			agentKey, err = agentKeyGenerator.GenerateAgentKey()
			if err != nil {
				return "", xerrors.Errorf("failed to generate agent key in OM: %w", err)
			}
			log.Info("Agent key was successfully generated")
		}

		agentSecret := secret.Builder().
			SetField(util.OmAgentApiKey, agentKey).
			SetNamespace(namespace).
			SetName(secretName).
			Build()

		if err := secretClient.PutSecret(ctx, agentSecret, basePath); err != nil {
			return "", xerrors.Errorf("failed to create AgentKey secret: %w", err)
		}

		log.Infof("Project agent key is saved for later usage")
		return agentKey, nil
	}

	return agentKeySecret[util.OmAgentApiKey], nil
}

// ApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func ApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

// WaitForRsAgentsToRegister waits until all the agents associated with the given StatefulSet have registered with Ops Manager.
func WaitForRsAgentsToRegister(set appsv1.StatefulSet, members int, clusterName string, omConnection om.Connection, log *zap.SugaredLogger, rs *mdbv1.MongoDB) error {
	hostnames, _ := dns.GetDnsForStatefulSetReplicasSpecified(set, clusterName, members, rs.Spec.DbCommonSpec.GetExternalDomain())

	log = log.With("statefulset", set.Name)

	ok, msg := waitUntilRegistered(omConnection, log, retryParams{retrials: 5, waitSeconds: 3}, hostnames...)
	if !ok {
		return getAgentRegisterError(msg)
	}
	return nil
}

// WaitForRsAgentsToRegisterSpecifiedHostnames waits for the specified agents to registry with Ops Manager.
func WaitForRsAgentsToRegisterSpecifiedHostnames(omConnection om.Connection, hostnames []string, log *zap.SugaredLogger) error {
	ok, msg := waitUntilRegistered(omConnection, log, retryParams{retrials: 10, waitSeconds: 9}, hostnames...)
	if !ok {
		return getAgentRegisterError(msg)
	}
	return nil
}

func getAgentRegisterError(errorMsg string) error {
	return xerrors.New(fmt.Sprintf("some agents failed to register or the Operator is using the wrong host names for the pods. "+
		"Make sure the 'spec.clusterDomain' is set if it's different from the default Kubernetes cluster "+
		"name ('cluster.local'): %s", errorMsg))
}

// waitUntilRegistered waits until all agents with 'agentHostnames' are registered in OM. Note, that wait
// happens after retrial - this allows to skip waiting in case agents are already registered
func waitUntilRegistered(omConnection om.Connection, log *zap.SugaredLogger, r retryParams, agentHostnames ...string) (bool, string) {
	if len(agentHostnames) == 0 {
		log.Debugf("Not waiting for agents as the agentHostnames list is empty")
		return true, "Not waiting for agents as the agentHostnames list is empty"
	}
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	// environment variables are used only for tests
	waitSeconds := env.ReadIntOrDefault(util.PodWaitSecondsEnv, r.waitSeconds)
	retrials := env.ReadIntOrDefault(util.PodWaitRetriesEnv, r.retrials)

	agentsCheckFunc := func() (string, bool) {
		registeredHostnamesMap := map[string]struct{}{}
		_, err := om.TraversePages(
			omConnection.ReadAutomationAgents,
			func(aa interface{}) bool {
				automationAgent := aa.(om.Status)
				for _, hostname := range agentHostnames {
					if automationAgent.IsRegistered(hostname, log) {
						registeredHostnamesMap[hostname] = struct{}{}
						if len(registeredHostnamesMap) == len(agentHostnames) {
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

		// convert to list of keys only for pretty printing in the error message
		var registeredHostnamesList []string
		for hostname := range registeredHostnamesMap {
			registeredHostnamesList = append(registeredHostnamesList, hostname)
		}

		var msg string
		if len(registeredHostnamesList) == 0 {
			return fmt.Sprintf("None of %d expected agents has registered with OM, expected hostnames: %+v", len(agentHostnames), agentHostnames), false
		} else if len(registeredHostnamesList) == len(agentHostnames) {
			return fmt.Sprintf("All of %d expected agents have registered with OM, hostnames: %+v", len(registeredHostnamesList), registeredHostnamesList), true
		} else {
			var missingHostnames []string
			for _, expectedHostname := range agentHostnames {
				if _, ok := registeredHostnamesMap[expectedHostname]; !ok {
					missingHostnames = append(missingHostnames, expectedHostname)
				}
			}
			msg = fmt.Sprintf("Only %d of %d expected agents have registered with OM, missing hostnames: %+v, registered hostnames in OM: %+v, expected hostnames: %+v", len(registeredHostnamesList), len(agentHostnames), missingHostnames, registeredHostnamesList, agentHostnames)
			return msg, false
		}
	}

	return util.DoAndRetry(agentsCheckFunc, log, retrials, waitSeconds)
}
