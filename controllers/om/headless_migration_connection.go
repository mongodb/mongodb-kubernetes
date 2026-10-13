package om

import (
	"sort"
	"time"

	"golang.org/x/xerrors"
	corev1 "k8s.io/api/core/v1"
)

// agentPodLister is implemented by connections that can enumerate the agent pods owned by the
// resource. It is used to tell which pods still run headless during a migration.
type agentPodLister interface {
	listAgentPods() ([]corev1.Pod, error)
}

// HeadlessMigrationConnection decorates the target Ops Manager connection while a headless MongoDB
// resource is being handed over to Ops Manager. All automation config reads and writes go to the
// target Ops Manager project (the embedded Connection); the agents that still run headless are
// evaluated against the automation config Secret they consume, whose version was captured at seed
// time (baselineVersion).
type HeadlessMigrationConnection struct {
	Connection
	headless        Connection
	baselineVersion int64
}

var _ Connection = &HeadlessMigrationConnection{}

// NewHeadlessMigrationConnection wraps the target Ops Manager connection. headless must be a
// connection that can list the resource's agent pods (e.g. a HeadlessConnection).
func NewHeadlessMigrationConnection(target Connection, headless Connection, baselineVersion int64) *HeadlessMigrationConnection {
	return &HeadlessMigrationConnection{
		Connection:      target,
		headless:        headless,
		baselineVersion: baselineVersion,
	}
}

// BaselineVersion returns the version of the headless automation config Secret at seed time.
func (c *HeadlessMigrationConnection) BaselineVersion() int64 {
	return c.baselineVersion
}

// ReadAutomationStatus merges the target Ops Manager status with the state of the pods that still
// run headless. A headless agent that reached the baseline version is considered at goal for the
// target project: its mounted configuration has not changed, so it does not need to fetch anything
// from Ops Manager. Headless agents that have not reached the baseline yet are reported as-is, so
// WaitForReadyState keeps waiting for them.
func (c *HeadlessMigrationConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	targetStatus, err := c.Connection.ReadAutomationStatus()
	if err != nil {
		return nil, err
	}

	headlessStatus, err := c.headless.ReadAutomationStatus()
	if err != nil {
		return nil, err
	}

	pods, err := c.listAgentPods()
	if err != nil {
		return nil, err
	}

	processes := make(map[string]ProcessStatus, len(targetStatus.Processes))
	for _, process := range targetStatus.Processes {
		processes[process.Name] = process
	}

	for _, headlessProcess := range headlessStatus.Processes {
		pod := podByProcessName(pods, headlessProcess.Name, headlessProcess.Hostname)
		if pod == nil || !IsHeadlessAgentPod(*pod) {
			continue
		}

		if int64(headlessProcess.LastGoalVersionAchieved) == c.baselineVersion {
			headlessProcess.LastGoalVersionAchieved = targetStatus.GoalVersion
		}
		processes[headlessProcess.Name] = headlessProcess
	}

	merged := make([]ProcessStatus, 0, len(processes))
	for _, process := range processes {
		merged = append(merged, process)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })

	return &AutomationStatus{GoalVersion: targetStatus.GoalVersion, Processes: merged}, nil
}

// ReadAutomationAgents returns the union of the agents registered with the target Ops Manager
// project and the headless agents synthesized from the pods that still run in headless mode. The
// headless agents never report to Ops Manager, so without the union the operator would wait for
// them to register forever. Pods that already run the online agent are intentionally not
// synthesized: their agents must actually register with the target project.
func (c *HeadlessMigrationConnection) ReadAutomationAgents(page int) (Paginated, error) {
	targetResponse, err := c.Connection.ReadAutomationAgents(page)
	if err != nil {
		return nil, err
	}
	targetAgents, ok := targetResponse.(AutomationAgentStatusResponse)
	if !ok {
		return nil, xerrors.Errorf("unexpected target automation agents response type %T", targetResponse)
	}

	deployment, err := c.headless.ReadDeployment()
	if err != nil {
		return nil, err
	}
	pods, err := c.listAgentPods()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(targetAgents.AutomationAgents))
	merged := make([]AgentStatus, 0, len(targetAgents.AutomationAgents)+deployment.NumberOfProcesses())
	for _, agent := range targetAgents.AutomationAgents {
		seen[agent.Hostname] = struct{}{}
		merged = append(merged, agent)
	}

	for _, process := range deployment.getProcesses() {
		if _, ok := seen[process.HostName()]; ok {
			continue
		}
		pod := podByProcessName(pods, process.Name(), process.HostName())
		if pod == nil || !IsHeadlessAgentPod(*pod) {
			continue
		}
		merged = append(merged, AgentStatus{
			Hostname:  process.HostName(),
			LastConf:  time.Now().UTC().Format(time.RFC3339),
			TypeName:  "AUTOMATION",
			StateName: "ACTIVE",
		})
	}

	return AutomationAgentStatusResponse{
		AutomationAgents: merged,
		OMPaginated:      OMPaginated{TotalCount: len(merged)},
	}, nil
}

func (c *HeadlessMigrationConnection) listAgentPods() ([]corev1.Pod, error) {
	lister, ok := c.headless.(agentPodLister)
	if !ok {
		return nil, xerrors.Errorf("headless connection %T cannot list agent pods", c.headless)
	}
	return lister.listAgentPods()
}
