package om

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesClient "sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/host"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

const (
	// HeadlessAgentVersionAnnotation is patched on each agent pod by the headless readiness
	// probe with the automation config version the agent last reached goal state for.
	HeadlessAgentVersionAnnotation = "agent.mongodb.com/version"

	// HeadlessAutomationConfigSecretSuffix is appended to the MongoDB resource name to form
	// the name of the Secret holding the automation config.
	HeadlessAutomationConfigSecretSuffix = "-config"

	// headlessFakeOpsManagerVersion is reported to code paths that gate behavior on the
	// Ops Manager version (static containers support, feature controls).
	headlessFakeOpsManagerVersion = "7.0.0"
)

// HeadlessContext carries the Kubernetes client and the resource identity needed by
// HeadlessConnection to persist the automation config in a Secret instead of Ops Manager.
type HeadlessContext struct {
	Client          kubernetesClient.Client
	Namespace       string
	Name            string
	SecretName      string
	AgentVersion    string
	OwnerReferences []metav1.OwnerReference
}

// HeadlessConnection is an om.Connection implementation for resources that are not managed by
// an Ops Manager instance. The automation config is stored verbatim (the same JSON document the
// agent consumes) in a Secret and the agents run in headless mode. Goal state is derived from
// the annotation the headless readiness probe patches onto the agent pods.
type HeadlessConnection struct {
	context *OMContext
}

var _ Connection = &HeadlessConnection{}

func NewHeadlessConnection(context *OMContext) Connection {
	return &HeadlessConnection{context: context}
}

// NewConnection returns a HeadlessConnection when the context carries headless information and
// an HTTP connection to Ops Manager otherwise.
func NewConnection(context *OMContext) Connection {
	if context != nil && context.Headless != nil {
		return NewHeadlessConnection(context)
	}
	return NewOpsManagerConnection(context)
}

func (hc *HeadlessConnection) headless() *HeadlessContext {
	return hc.context.Headless
}

func (hc *HeadlessConnection) secretName() string {
	if name := hc.headless().SecretName; name != "" {
		return name
	}
	return hc.headless().Name + HeadlessAutomationConfigSecretSuffix
}

// *************************************** deployment persistence ***************************************

const headlessDefaultDownloadBase = "/var/lib/mongodb-mms-automation"

// normalizeHeadlessDeployment makes the deployment acceptable to the automation agent's cluster
// config schema: `options` and `mongoDbVersions` are required top-level fields and a `tls` object
// is only valid when it carries a CA file path.
func normalizeHeadlessDeployment(deployment Deployment) {
	if options, ok := deployment["options"].(map[string]interface{}); !ok || (options["downloadBase"] == nil && options["downloadBaseWindows"] == nil) {
		deployment["options"] = map[string]interface{}{"downloadBase": headlessDefaultDownloadBase}
	}

	if !hasMongoDbVersions(deployment) {
		deployment["mongoDbVersions"] = headlessMongoDbVersions(deployment)
	}

	if tlsConfig, ok := deployment["tls"].(map[string]interface{}); ok {
		if tlsConfig["CAFilePath"] == nil && tlsConfig["CAFilePathWindows"] == nil {
			delete(deployment, "tls")
		}
	}
}

func hasMongoDbVersions(deployment Deployment) bool {
	switch versions := deployment["mongoDbVersions"].(type) {
	case []interface{}:
		return len(versions) > 0
	case []MongoDbVersionConfig:
		return len(versions) > 0
	default:
		return false
	}
}

// headlessMongoDbVersions builds schema-valid version manifests for every mongod version used by
// the deployment. The agent in static architecture takes the binaries from the container image,
// so the builds only need to satisfy the config schema.
func headlessMongoDbVersions(deployment Deployment) []map[string]interface{} {
	build := func(flavor string, modules []string) map[string]interface{} {
		return map[string]interface{}{
			"platform":     "linux",
			"architecture": "amd64",
			"flavor":       flavor,
			"url":          "",
			"gitVersion":   "",
			"modules":      modules,
		}
	}

	versions := make([]map[string]interface{}, 0)
	seen := map[string]struct{}{}
	for _, process := range deployment.getProcesses() {
		version, _ := process["version"].(string)
		if version == "" {
			continue
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}

		modules := []string{}
		if strings.HasSuffix(version, "-ent") {
			modules = []string{"enterprise"}
		}
		versions = append(versions, map[string]interface{}{
			"name":   version,
			"builds": []map[string]interface{}{build("rhel", modules), build("ubuntu", modules)},
		})
	}
	return versions
}

// readDeployment returns the automation config from the Secret. If the Secret (or the config key)
// does not exist yet, an empty deployment is returned with exists=false.
func (hc *HeadlessConnection) readDeployment() (Deployment, bool, error) {
	secret := &corev1.Secret{}
	err := hc.headless().Client.Get(context.Background(), types.NamespacedName{
		Namespace: hc.headless().Namespace,
		Name:      hc.secretName(),
	}, secret)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return NewDeployment(), false, nil
		}
		return nil, false, xerrors.Errorf("failed to read automation config secret %s/%s: %w", hc.headless().Namespace, hc.secretName(), err)
	}

	raw := secret.Data[automationconfig.ConfigKey]
	if len(raw) == 0 {
		return NewDeployment(), true, nil
	}

	deployment, err := BuildDeploymentFromBytes(raw)
	if err != nil {
		return nil, false, xerrors.Errorf("failed to parse automation config secret %s/%s: %w", hc.headless().Namespace, hc.secretName(), err)
	}

	return deployment, true, nil
}

// writeDeployment persists the deployment into the Secret, creating it if necessary.
func (hc *HeadlessConnection) writeDeployment(deployment Deployment, exists bool) ([]byte, error) {
	raw, err := deployment.Serialize()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if !exists {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            hc.secretName(),
				Namespace:       hc.headless().Namespace,
				OwnerReferences: hc.headless().OwnerReferences,
			},
			Data: map[string][]byte{automationconfig.ConfigKey: raw},
		}
		if err := hc.headless().Client.Create(ctx, secret); err != nil {
			return nil, xerrors.Errorf("failed to create automation config secret %s/%s: %w", hc.headless().Namespace, hc.secretName(), err)
		}
		return raw, nil
	}

	secret := &corev1.Secret{}
	if err := hc.headless().Client.Get(ctx, types.NamespacedName{
		Namespace: hc.headless().Namespace,
		Name:      hc.secretName(),
	}, secret); err != nil {
		return nil, xerrors.Errorf("failed to read automation config secret %s/%s: %w", hc.headless().Namespace, hc.secretName(), err)
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[automationconfig.ConfigKey] = raw
	if len(secret.OwnerReferences) == 0 {
		secret.OwnerReferences = hc.headless().OwnerReferences
	}

	if err := hc.headless().Client.Update(ctx, secret); err != nil {
		return nil, xerrors.Errorf("failed to update automation config secret %s/%s: %w", hc.headless().Namespace, hc.secretName(), err)
	}

	return raw, nil
}

// withoutVersion returns a copy of the deployment without the version field for change detection.
func withoutVersion(deployment Deployment) Deployment {
	result := make(Deployment, len(deployment))
	for key, value := range deployment {
		if key == "version" {
			continue
		}
		result[key] = value
	}
	return result
}

func (hc *HeadlessConnection) UpdateDeployment(deployment Deployment) ([]byte, error) {
	normalizeHeadlessDeployment(deployment)

	stored, exists, err := hc.readDeployment()
	if err != nil {
		return nil, err
	}
	normalizeHeadlessDeployment(stored)

	if exists {
		storedVersion := stored.Version()
		if storedVersion < 0 {
			storedVersion = 0
		}
		if reflect.DeepEqual(withoutVersion(deployment.ToCanonicalForm()), withoutVersion(stored.ToCanonicalForm())) {
			// No semantic change: keep the current version and avoid waking the agents up.
			deployment["version"] = storedVersion
			return deployment.Serialize()
		}
		deployment["version"] = storedVersion + 1
	} else if deployment.Version() < 1 {
		deployment["version"] = 1
	}

	return hc.writeDeployment(deployment, exists)
}

func (hc *HeadlessConnection) ReadDeployment() (Deployment, error) {
	deployment, _, err := hc.readDeployment()
	return deployment, err
}

func (hc *HeadlessConnection) ReadUpdateDeployment(changeDeploymentFunc func(Deployment) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(hc.GroupName(), hc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	deployment, _, err := hc.readDeployment()
	if err != nil {
		return err
	}

	if err := changeDeploymentFunc(deployment); err != nil {
		return err
	}

	_, err = hc.UpdateDeployment(deployment)
	return err
}

func (hc *HeadlessConnection) ReadAutomationConfig() (*AutomationConfig, error) {
	deployment, err := hc.ReadDeployment()
	if err != nil {
		return nil, err
	}
	return BuildAutomationConfigFromDeployment(deployment)
}

func (hc *HeadlessConnection) UpdateAutomationConfig(ac *AutomationConfig, log *zap.SugaredLogger) error {
	if err := ac.Apply(); err != nil {
		return err
	}
	_, err := hc.UpdateDeployment(ac.Deployment)
	return err
}

func (hc *HeadlessConnection) ReadUpdateAutomationConfig(modifyACFunc func(ac *AutomationConfig) error, log *zap.SugaredLogger) error {
	mutex := GetMutex(hc.GroupName(), hc.OrgID())
	mutex.Lock()
	defer mutex.Unlock()

	ac, err := hc.ReadAutomationConfig()
	if err != nil {
		return err
	}

	if err := modifyACFunc(ac); err != nil {
		return err
	}

	if err := ac.Apply(); err != nil {
		return err
	}

	_, err = hc.UpdateDeployment(ac.Deployment)
	return err
}

// *************************************** agent status ***************************************

// listAgentPods returns the pods owned by this resource's StatefulSet.
func (hc *HeadlessConnection) listAgentPods() ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := hc.headless().Client.List(context.Background(), pods, kubernetesClient.InNamespace(hc.headless().Namespace)); err != nil {
		return nil, xerrors.Errorf("failed to list agent pods in %s: %w", hc.headless().Namespace, err)
	}

	result := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if podOwnedByStatefulSet(pod, hc.headless().Name) {
			result = append(result, pod)
		}
	}
	return result, nil
}

func podOwnedByStatefulSet(pod corev1.Pod, name string) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "StatefulSet" && ref.Name == name {
			return true
		}
	}
	return false
}

func podByProcessName(pods []corev1.Pod, processName, hostname string) *corev1.Pod {
	for i := range pods {
		if pods[i].Name == processName || pods[i].Name == hostname {
			return &pods[i]
		}
	}
	return nil
}

func (hc *HeadlessConnection) ReadAutomationStatus() (*AutomationStatus, error) {
	deployment, err := hc.ReadDeployment()
	if err != nil {
		return nil, err
	}

	goalVersion := int(deployment.Version())
	if goalVersion < 0 {
		goalVersion = 0
	}

	pods, err := hc.listAgentPods()
	if err != nil {
		return nil, err
	}

	// Only processes whose pods exist are reported: before a pod is created the agent has not
	// registered, which mirrors Ops Manager only reporting agents that have checked in.
	processes := make([]ProcessStatus, 0, len(pods))
	for _, process := range deployment.getProcesses() {
		pod := podByProcessName(pods, process.Name(), process.HostName())
		if pod == nil {
			continue
		}

		achieved := 0
		if annotation, ok := pod.Annotations[HeadlessAgentVersionAnnotation]; ok {
			if parsed, err := strconv.Atoi(annotation); err == nil {
				achieved = parsed
			}
		}

		processes = append(processes, ProcessStatus{
			Name:                    process.Name(),
			Hostname:                process.HostName(),
			LastGoalVersionAchieved: achieved,
		})
	}

	return &AutomationStatus{GoalVersion: goalVersion, Processes: processes}, nil
}

func (hc *HeadlessConnection) ReadAutomationAgents(pageNum int) (Paginated, error) {
	deployment, err := hc.ReadDeployment()
	if err != nil {
		return nil, err
	}

	pods, err := hc.listAgentPods()
	if err != nil {
		return nil, err
	}

	// Agents are only reported once their pod exists; hostnames mirror the automation config
	// process hostnames, which is what the operator matches registration against.
	agents := make([]AgentStatus, 0, len(pods))
	for _, process := range deployment.getProcesses() {
		if podByProcessName(pods, process.Name(), process.HostName()) == nil {
			continue
		}
		agents = append(agents, AgentStatus{
			Hostname:  process.HostName(),
			LastConf:  time.Now().UTC().Format(time.RFC3339),
			TypeName:  "AUTOMATION",
			StateName: "ACTIVE",
		})
	}

	return AutomationAgentStatusResponse{
		AutomationAgents: agents,
		OMPaginated:      OMPaginated{TotalCount: len(agents)},
	}, nil
}

// *************************************** version / keys / features ***************************************

func (hc *HeadlessConnection) OpsManagerVersion() versionutil.OpsManagerVersion {
	return versionutil.OpsManagerVersion{VersionString: headlessFakeOpsManagerVersion}
}

func (hc *HeadlessConnection) ReadAgentVersion() (AgentsVersionsResponse, error) {
	return AgentsVersionsResponse{
		AutomationVersion:        hc.headless().AgentVersion,
		AutomationMinimumVersion: hc.headless().AgentVersion,
	}, nil
}

func (hc *HeadlessConnection) GenerateAgentKey() (string, error) {
	return uuid.New().String(), nil
}

func (hc *HeadlessConnection) GetAgentAuthMode() (string, error) {
	ac, err := hc.ReadAutomationConfig()
	if err != nil {
		return "", err
	}
	if ac.Auth == nil {
		return "", nil
	}
	return ac.Auth.AutoAuthMechanism, nil
}

func (hc *HeadlessConnection) GetControlledFeature() (*controlledfeature.ControlledFeature, error) {
	return &controlledfeature.ControlledFeature{}, nil
}

func (hc *HeadlessConnection) UpdateControlledFeature(cf *controlledfeature.ControlledFeature) error {
	return nil
}

func (hc *HeadlessConnection) UpgradeAgentsToLatest() (string, error) {
	return "", xerrors.New("agent upgrades are not supported in headless mode")
}

// *************************************** hosts ***************************************

func (hc *HeadlessConnection) GetHosts() (*host.Result, error) {
	deployment, err := hc.ReadDeployment()
	if err != nil {
		return nil, err
	}

	results := make([]host.Host, 0, deployment.NumberOfProcesses())
	for _, process := range deployment.getProcesses() {
		results = append(results, host.Host{Id: process.Name(), Hostname: process.HostName()})
	}

	return &host.Result{Results: results}, nil
}

func (hc *HeadlessConnection) AddHost(host host.Host) error {
	return nil
}

func (hc *HeadlessConnection) UpdateHost(host host.Host) error {
	return nil
}

func (hc *HeadlessConnection) RemoveHost(hostID string) error {
	return nil
}

func (hc *HeadlessConnection) GetPreferredHostnames(agentApiKey string) ([]PreferredHostname, error) {
	return nil, nil
}

func (hc *HeadlessConnection) AddPreferredHostname(agentApiKey string, value string, isRegexp bool) error {
	return nil
}

// *************************************** project / organization ***************************************

func (hc *HeadlessConnection) organization() *Organization {
	return &Organization{ID: hc.GroupID(), Name: hc.GroupName()}
}

func (hc *HeadlessConnection) project() *Project {
	return &Project{ID: hc.GroupID(), Name: hc.GroupName(), OrgID: hc.GroupID()}
}

func (hc *HeadlessConnection) ReadOrganizationsByName(name string) ([]*Organization, error) {
	return []*Organization{hc.organization()}, nil
}

func (hc *HeadlessConnection) ReadOrganizations(page int) (Paginated, error) {
	return &OrganizationsResponse{
		Organizations: []*Organization{hc.organization()},
		OMPaginated:   OMPaginated{TotalCount: 1},
	}, nil
}

func (hc *HeadlessConnection) ReadOrganization(orgID string) (*Organization, error) {
	return hc.organization(), nil
}

func (hc *HeadlessConnection) ReadProjectsInOrganizationByName(orgID string, name string) ([]*Project, error) {
	return []*Project{hc.project()}, nil
}

func (hc *HeadlessConnection) ReadProjectsInOrganization(orgID string, page int) (Paginated, error) {
	return &ProjectsResponse{
		Groups:      []*Project{hc.project()},
		OMPaginated: OMPaginated{TotalCount: 1},
	}, nil
}

func (hc *HeadlessConnection) CreateProject(project *Project) (*Project, error) {
	return project, nil
}

func (hc *HeadlessConnection) UpdateProject(project *Project) (*Project, error) {
	return project, nil
}

func (hc *HeadlessConnection) MarkProjectAsBackingDatabase(databaseType BackingDatabaseType) error {
	return nil
}

// *************************************** agent config surfaces ***************************************

func (hc *HeadlessConnection) ReadUpdateAgentsLogRotation(logRotateSetting mdbv1.AgentConfig, log *zap.SugaredLogger) error {
	return nil
}

func (hc *HeadlessConnection) ReadMonitoringAgentConfig() (*MonitoringAgentConfig, error) {
	return &MonitoringAgentConfig{MonitoringAgentTemplate: &MonitoringAgentTemplate{}}, nil
}

func (hc *HeadlessConnection) UpdateMonitoringAgentConfig(mat *MonitoringAgentConfig, log *zap.SugaredLogger) ([]byte, error) {
	return nil, nil
}

func (hc *HeadlessConnection) ReadUpdateMonitoringAgentConfig(matFunc func(*MonitoringAgentConfig) error, log *zap.SugaredLogger) error {
	return nil
}

func (hc *HeadlessConnection) ReadBackupAgentConfig() (*BackupAgentConfig, error) {
	return &BackupAgentConfig{BackupAgentTemplate: &BackupAgentTemplate{}}, nil
}

func (hc *HeadlessConnection) UpdateBackupAgentConfig(bac *BackupAgentConfig, log *zap.SugaredLogger) ([]byte, error) {
	return nil, nil
}

func (hc *HeadlessConnection) ReadUpdateBackupAgentConfig(bacFunc func(*BackupAgentConfig) error, log *zap.SugaredLogger) error {
	return nil
}

// *************************************** unsupported (Ops Manager only) ***************************************

const headlessUnsupportedMessage = "operation is not supported for resources not managed by Ops Manager"

func headlessUnsupported(operation string) error {
	return xerrors.Errorf("%s: %s", operation, headlessUnsupportedMessage)
}

func (hc *HeadlessConnection) ReadGroupBackupConfig() (backup.GroupBackupConfig, error) {
	return backup.GroupBackupConfig{}, headlessUnsupported("read group backup config")
}

func (hc *HeadlessConnection) UpdateGroupBackupConfig(config backup.GroupBackupConfig) ([]byte, error) {
	return nil, headlessUnsupported("update group backup config")
}

func (hc *HeadlessConnection) ReadHostCluster(clusterID string) (*backup.HostCluster, error) {
	return nil, headlessUnsupported("read host cluster")
}

func (hc *HeadlessConnection) ReadBackupConfigs() (*backup.ConfigsResponse, error) {
	return nil, headlessUnsupported("read backup configs")
}

func (hc *HeadlessConnection) ReadBackupConfig(clusterID string) (*backup.Config, error) {
	return nil, headlessUnsupported("read backup config")
}

func (hc *HeadlessConnection) UpdateBackupConfig(config *backup.Config) (*backup.Config, error) {
	return nil, headlessUnsupported("update backup config")
}

func (hc *HeadlessConnection) UpdateBackupStatus(clusterID string, status backup.Status) error {
	return headlessUnsupported("update backup status")
}

func (hc *HeadlessConnection) ReadSnapshotSchedule(clusterID string) (*backup.SnapshotSchedule, error) {
	return nil, headlessUnsupported("read snapshot schedule")
}

func (hc *HeadlessConnection) UpdateSnapshotSchedule(clusterID string, snapshotSchedule *backup.SnapshotSchedule) error {
	return headlessUnsupported("update snapshot schedule")
}

// *************************************** identity ***************************************

func (hc *HeadlessConnection) BaseURL() string {
	return ""
}

func (hc *HeadlessConnection) GroupID() string {
	return hc.context.GroupID
}

func (hc *HeadlessConnection) GroupName() string {
	return hc.context.GroupName
}

func (hc *HeadlessConnection) OrgID() string {
	return hc.context.OrgID
}

func (hc *HeadlessConnection) PublicKey() string {
	return ""
}

func (hc *HeadlessConnection) PrivateKey() string {
	return ""
}

func (hc *HeadlessConnection) ConfigureProject(project *Project) {
	hc.context.GroupID = project.ID
	hc.context.OrgID = project.OrgID
}
