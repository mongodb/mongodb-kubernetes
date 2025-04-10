package agents

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
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

const RollingChangeArgs = "RollingChangeArgs"

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

const StaleProcessDuration = time.Minute * 2

// ProcessState represents the state of the mongodb process.
// Most importantly it contains the information whether the node is down (precisely whether the agent running next to mongod is actively reporting pings to OM),
// what is the last version of the automation config achieved and the step on which the agent is currently executing the plan.
type ProcessState struct {
	Hostname            string
	LastAgentPing       time.Time
	GoalVersionAchieved int
	Plan                []string
	ProcessName         string
}

// NewProcessState should be used to create new instances of ProcessState as it sets some reasonable default values.
// As ProcessState is combining the data from two sources, we don't have any guarantees that we'll have the information about the given hostname
// available from both sources, therefore we need to always assume some defaults.
func NewProcessState(hostname string) ProcessState {
	return ProcessState{
		Hostname:            hostname,
		LastAgentPing:       time.Time{},
		GoalVersionAchieved: -1,
		Plan:                nil,
	}
}

// IsStale returns true if this process is considered down, i.e. last ping of the agent is later than 2 minutes ago
// We use an in-the-middle value when considering the process to be down:
//   - in waitForAgentsToRegister we use 1 min to consider the process "not registered"
//   - Ops Manager is using 5 mins as a default for considering process as stale
func (p ProcessState) IsStale() bool {
	return p.LastAgentPing.Add(StaleProcessDuration).Before(time.Now())
}

// MongoDBClusterStateInOM represents the state of the whole deployment from the Ops Manager's perspective by combining singnals about the processes from two sources:
//   - from om.Connection.ReadAutomationAgents to get last ping of the agent (/groups/<groupId>/agents/AUTOMATION)
//   - from om.Connection.ReadAutomationStatus to get the list of agent health statuses, AC version achieved, step of the agent's plan (/groups/<groupId>/automationStatus)
type MongoDBClusterStateInOM struct {
	GoalVersion     int
	ProcessStateMap map[string]ProcessState
}

// GetMongoDBClusterState executes requests to OM from the given omConnection to gather the current deployment state.
// It combines the data from the automation status and the list of automation agents.
func GetMongoDBClusterState(omConnection om.Connection) (MongoDBClusterStateInOM, error) {
	var agentStatuses []om.AgentStatus
	_, err := om.TraversePages(
		omConnection.ReadAutomationAgents,
		func(aa interface{}) bool {
			agentStatuses = append(agentStatuses, aa.(om.AgentStatus))
			return false
		},
	)
	if err != nil {
		return MongoDBClusterStateInOM{}, xerrors.Errorf("error when reading automation agent pages: %v", err)
	}

	automationStatus, err := omConnection.ReadAutomationStatus()
	if err != nil {
		return MongoDBClusterStateInOM{}, xerrors.Errorf("error reading automation status: %v", err)
	}

	processStateMap, err := calculateProcessStateMap(automationStatus.Processes, agentStatuses)
	if err != nil {
		return MongoDBClusterStateInOM{}, err
	}

	return MongoDBClusterStateInOM{
		GoalVersion:     automationStatus.GoalVersion,
		ProcessStateMap: processStateMap,
	}, nil
}

func (c *MongoDBClusterStateInOM) GetProcessState(hostname string) ProcessState {
	if processState, ok := c.ProcessStateMap[hostname]; ok {
		return processState
	}

	return NewProcessState(hostname)
}

func (c *MongoDBClusterStateInOM) GetProcesses() []ProcessState {
	return slices.Collect(maps.Values(c.ProcessStateMap))
}

func (c *MongoDBClusterStateInOM) GetProcessesNotInGoalState() []ProcessState {
	return slices.DeleteFunc(slices.Collect(maps.Values(c.ProcessStateMap)), func(processState ProcessState) bool {
		return processState.GoalVersionAchieved >= c.GoalVersion
	})
}

// calculateProcessStateMap combines information from ProcessStatuses and AgentStatuses returned by OpsManager
// and maps them to a unified data structure.
//
// The resulting ProcessState combines information from both agent and process status when refer to the same hostname.
// It is not guaranteed that we'll have the information from two sources, so in case one side is missing the defaults
// would be present as defined in NewProcessState.
// If multiple statuses exist for the same hostname, subsequent entries overwrite ones.
// Fields such as GoalVersionAchieved default to -1 if never set, and Plan defaults to nil.
// LastAgentPing defaults to the zero time if no AgentStatus entry is available.
func calculateProcessStateMap(processStatuses []om.ProcessStatus, agentStatuses []om.AgentStatus) (map[string]ProcessState, error) {
	processStates := map[string]ProcessState{}
	for _, agentStatus := range agentStatuses {
		if agentStatus.TypeName != "AUTOMATION" {
			return nil, xerrors.Errorf("encountered unexpected agent type in agent status type in %+v", agentStatus)
		}
		processState, ok := processStates[agentStatus.Hostname]
		if !ok {
			processState = NewProcessState(agentStatus.Hostname)
		}
		lastPing, err := time.Parse(time.RFC3339, agentStatus.LastConf)
		if err != nil {
			return nil, xerrors.Errorf("wrong format for lastConf field: expected UTC format but the value is %s, agentStatus=%+v: %v", agentStatus.LastConf, agentStatus, err)
		}
		processState.LastAgentPing = lastPing

		processStates[agentStatus.Hostname] = processState
	}

	for _, processStatus := range processStatuses {
		processState, ok := processStates[processStatus.Hostname]
		if !ok {
			processState = NewProcessState(processStatus.Hostname)
		}
		processState.GoalVersionAchieved = processStatus.LastGoalVersionAchieved
		processState.ProcessName = processStatus.Name
		processState.Plan = processStatus.Plan
		processStates[processStatus.Hostname] = processState
	}

	return processStates, nil
}

func agentCheck(omConnection om.Connection, agentHostnames []string, log *zap.SugaredLogger) (string, bool) {
	registeredHostnamesSet := map[string]struct{}{}
	predicateFunc := func(aa interface{}) bool {
		automationAgent := aa.(om.Status)
		for _, hostname := range agentHostnames {
			if automationAgent.IsRegistered(hostname, log) {
				registeredHostnamesSet[hostname] = struct{}{}
				if len(registeredHostnamesSet) == len(agentHostnames) {
					return true
				}
			}
		}
		return false
	}

	_, err := om.TraversePages(
		omConnection.ReadAutomationAgents,
		predicateFunc,
	)
	if err != nil {
		return fmt.Sprintf("Received error when reading automation agent pages: %v", err), false
	}

	// convert to list of keys only for pretty printing in the error message
	var registeredHostnamesList []string
	for hostname := range registeredHostnamesSet {
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
			if _, ok := registeredHostnamesSet[expectedHostname]; !ok {
				missingHostnames = append(missingHostnames, expectedHostname)
			}
		}
		msg = fmt.Sprintf("Only %d of %d expected agents have registered with OM, missing hostnames: %+v, registered hostnames in OM: %+v, expected hostnames: %+v", len(registeredHostnamesList), len(agentHostnames), missingHostnames, registeredHostnamesList, agentHostnames)
		return msg, false
	}
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
	waitSeconds := env.ReadIntOrDefault(util.PodWaitSecondsEnv, r.waitSeconds) // nolint:forbidigo
	retrials := env.ReadIntOrDefault(util.PodWaitRetriesEnv, r.retrials)       // nolint:forbidigo

	agentsCheckFunc := func() (string, bool) {
		return agentCheck(omConnection, agentHostnames, log)
	}

	return util.DoAndRetry(agentsCheckFunc, log, retrials, waitSeconds)
}
