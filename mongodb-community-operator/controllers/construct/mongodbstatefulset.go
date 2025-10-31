package construct

import (
	"fmt"
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/persistentvolumeclaim"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/resourcerequirements"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/readiness/config"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var OfficialMongodbRepoUrls = []string{"docker.io/mongodb", "quay.io/mongodb"}

// Environment variables used to configure the MongoDB StatefulSet.
const (
	MongodbRepoUrlEnv   = "MONGODB_REPO_URL"
	MongodbImageEnv     = "MONGODB_IMAGE"
	MongoDBImageTypeEnv = "MDB_IMAGE_TYPE"
	AgentImageEnv       = "AGENT_IMAGE"
)

// MCO only
const (
	VersionUpgradeHookImageEnv   = "VERSION_UPGRADE_HOOK_IMAGE"
	ReadinessProbeImageEnv       = "READINESS_PROBE_IMAGE"
	MongoDBCommunityImageTypeEnv = "MDB_COMMUNITY_IMAGE_TYPE"
	MongodbCommunityImageEnv     = "MDB_COMMUNITY_IMAGE"
	MongodbCommunityRepoUrlEnv   = "MDB_COMMUNITY_REPO_URL"
)

const (
	AgentName   = "mongodb-agent"
	MongodbName = "mongod"

	DefaultImageType = "ubi8"

	versionUpgradeHookName            = "mongod-posthook"
	ReadinessProbeContainerName       = "mongodb-agent-readinessprobe"
	readinessProbePath                = "/opt/scripts/readinessprobe"
	agentHealthStatusFilePathEnv      = "AGENT_STATUS_FILEPATH"
	clusterFilePath                   = "/var/lib/automation/config/cluster-config.json"
	mongodbDatabaseServiceAccountName = "mongodb-kubernetes-appdb"
	agentHealthStatusFilePathValue    = "/var/log/mongodb-mms-automation/healthstatus/agent-health-status.json"

	OfficialMongodbEnterpriseServerImageName = "mongodb-enterprise-server"

	headlessAgentEnv           = "HEADLESS_AGENT"
	podNamespaceEnv            = "POD_NAMESPACE"
	automationConfigEnv        = "AUTOMATION_CONFIG_MAP"
	MongoDBAssumeEnterpriseEnv = "MDB_ASSUME_ENTERPRISE"

	automationMongodConfFileName = "automation-mongod.conf"
	keyfileFilePath              = "/var/lib/mongodb-mms-automation/authentication/keyfile"

	automationAgentOptions = " -skipMongoStart -noDaemonize -useLocalMongoDbTools"

	MongodbUserCommand = `current_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
echo "mongodb:x:$(id -u):$(id -g):,,,:/:/bin/bash" >> /tmp/passwd
export NSS_WRAPPER_PASSWD=/tmp/passwd
export LD_PRELOAD=libnss_wrapper.so
export NSS_WRAPPER_GROUP=/etc/group
fi
`
)

// MongoDBStatefulSetOwner is an interface which any resource which generates a MongoDB StatefulSet should implement.
type MongoDBStatefulSetOwner interface {
	// ServiceName returns the name of the K8S service the operator will create.
	ServiceName() string
	// GetName returns the name of the resource.
	GetName() string
	// GetNamespace returns the namespace the resource is defined in.
	GetNamespace() string
	// GetMongoDBVersion returns the version of MongoDB to be used for this resource.
	GetMongoDBVersion() string
	// AutomationConfigSecretName returns the name of the secret which will contain the automation config.
	AutomationConfigSecretName() string
	// GetUpdateStrategyType returns the UpdateStrategyType of the statefulset.
	GetUpdateStrategyType() appsv1.StatefulSetUpdateStrategyType
	// HasSeparateDataAndLogsVolumes returns whether or not the volumes for data and logs would need to be different.
	HasSeparateDataAndLogsVolumes() bool
	// GetAgentKeyfileSecretNamespacedName returns the NamespacedName of the secret which stores the keyfile for the agent.
	GetAgentKeyfileSecretNamespacedName() types.NamespacedName
	// DataVolumeName returns the name that the data volume should have.
	DataVolumeName() string
	// LogsVolumeName returns the name that the data volume should have.
	LogsVolumeName() string
	// GetAgentLogLevel returns the log level for the MongoDB automation agent.
	GetAgentLogLevel() mdbv1.LogLevel
	// GetAgentLogFile returns the log file for the MongoDB automation agent.
	GetAgentLogFile() string
	// GetAgentMaxLogFileDurationHours returns the number of hours after which the log file should be rolled.
	GetAgentMaxLogFileDurationHours() int

	// GetMongodConfiguration returns the MongoDB configuration for each member.
	GetMongodConfiguration() mdbv1.MongodConfiguration

	// NeedsAutomationConfigVolume returns whether the statefulset needs to have a volume for the automationconfig.
	NeedsAutomationConfigVolume() bool
}

// BuildMongoDBReplicaSetStatefulSetModificationFunction builds the parts of the replica set that are common between every resource that implements
// MongoDBStatefulSetOwner.
// It doesn't configure TLS or additional containers/env vars that the statefulset might need.
func BuildMongoDBReplicaSetStatefulSetModificationFunction(mdb MongoDBStatefulSetOwner, scaler scale.ReplicaSetScaler, mongodbImage, agentImage, versionUpgradeHookImage, readinessProbeImage string, withInitContainers bool, initAppDBImage string, isArbiter bool) statefulset.Modification {
	labels := map[string]string{
		"app": mdb.ServiceName(),
	}

	// the health status volume is required in both agent and mongod pods.
	// the mongod requires it to determine if an upgrade is happening and needs to kill the pod
	// to prevent agent deadlock
	healthStatusVolume := statefulset.CreateVolumeFromEmptyDir("healthstatus")
	agentHealthStatusVolumeMount := statefulset.CreateVolumeMount(healthStatusVolume.Name, "/var/log/mongodb-mms-automation/healthstatus")
	mongodHealthStatusVolumeMount := statefulset.CreateVolumeMount(healthStatusVolume.Name, "/healthstatus")

	hooksVolume := corev1.Volume{}
	scriptsVolume := corev1.Volume{}
	upgradeInitContainer := podtemplatespec.NOOP()
	readinessInitContainer := podtemplatespec.NOOP()

	// tmp volume is required by the mongodb-agent and mongod
	tmpVolume := statefulset.CreateVolumeFromEmptyDir("tmp")
	tmpVolumeMount := statefulset.CreateVolumeMount(tmpVolume.Name, "/tmp", statefulset.WithReadOnly(false))

	keyFileNsName := mdb.GetAgentKeyfileSecretNamespacedName()
	keyFileVolume := statefulset.CreateVolumeFromEmptyDir(keyFileNsName.Name)
	keyFileVolumeVolumeMount := statefulset.CreateVolumeMount(keyFileVolume.Name, "/var/lib/mongodb-mms-automation/authentication", statefulset.WithReadOnly(false))
	keyFileVolumeVolumeMountMongod := statefulset.CreateVolumeMount(keyFileVolume.Name, "/var/lib/mongodb-mms-automation/authentication", statefulset.WithReadOnly(false))

	mongodbAgentVolumeMounts := []corev1.VolumeMount{agentHealthStatusVolumeMount, keyFileVolumeVolumeMount, tmpVolumeMount}

	automationConfigVolumeFunc := podtemplatespec.NOOP()
	if mdb.NeedsAutomationConfigVolume() {
		automationConfigVolume := statefulset.CreateVolumeFromSecret("automation-config", mdb.AutomationConfigSecretName())
		automationConfigVolumeFunc = podtemplatespec.WithVolume(automationConfigVolume)
		automationConfigVolumeMount := statefulset.CreateVolumeMount(automationConfigVolume.Name, "/var/lib/automation/config", statefulset.WithReadOnly(true))
		mongodbAgentVolumeMounts = append(mongodbAgentVolumeMounts, automationConfigVolumeMount)
	}
	mongodVolumeMounts := []corev1.VolumeMount{mongodHealthStatusVolumeMount, keyFileVolumeVolumeMountMongod, tmpVolumeMount}

	hooksVolume = statefulset.CreateVolumeFromEmptyDir("hooks")
	hooksVolumeMount := statefulset.CreateVolumeMount(hooksVolume.Name, "/hooks", statefulset.WithReadOnly(false))
	scriptsVolume = statefulset.CreateVolumeFromEmptyDir("agent-scripts")
	scriptsVolumeMount := statefulset.CreateVolumeMount(scriptsVolume.Name, "/opt/scripts", statefulset.WithReadOnly(false))

	scriptsVolumeMod := podtemplatespec.WithVolume(scriptsVolume)
	hooksVolumeMod := podtemplatespec.WithVolume(hooksVolume)
	withStaticContainerModification := podtemplatespec.NOOP()
	shareProcessNs := statefulset.NOOP()

	// we need the upgrade hook and readinessProbe either via init containers or via a side-car and /proc access
	// if we don't use init containers we need to use static containers
	if withInitContainers {
		mongodVolumeMounts = append(mongodVolumeMounts, hooksVolumeMount)
		mongodbAgentVolumeMounts = append(mongodbAgentVolumeMounts, scriptsVolumeMount)
		upgradeInitContainer = podtemplatespec.WithInitContainer(versionUpgradeHookName, versionUpgradeHookInit([]corev1.VolumeMount{hooksVolumeMount}, versionUpgradeHookImage))
		readinessInitContainer = podtemplatespec.WithInitContainer(ReadinessProbeContainerName, readinessProbeInit([]corev1.VolumeMount{scriptsVolumeMount}, readinessProbeImage))
	} else {
		staticMounts := []corev1.VolumeMount{hooksVolumeMount, scriptsVolumeMount, tmpVolumeMount}
		withStaticContainerModification = podtemplatespec.WithContainer(util.AgentContainerUtilitiesName, mongodbAgentUtilitiesContainer(staticMounts, initAppDBImage))
		mongodbAgentVolumeMounts = append(mongodbAgentVolumeMounts, staticMounts...)
		shareProcessNs = func(sts *appsv1.StatefulSet) {
			sts.Spec.Template.Spec.ShareProcessNamespace = ptr.To(true)
		}
	}

	dataVolumeClaim := statefulset.NOOP()
	logVolumeClaim := statefulset.NOOP()
	singleModeVolumeClaim := func(s *appsv1.StatefulSet) {}
	if !isArbiter {
		buildVolumesForMembers(mdb, &dataVolumeClaim, &logVolumeClaim, &singleModeVolumeClaim, &mongodbAgentVolumeMounts, &mongodVolumeMounts)
	}

	podSecurityContext, _ := podtemplatespec.WithDefaultSecurityContextsModifications()

	agentLogLevel := mdbv1.LogLevelInfo
	if mdb.GetAgentLogLevel() != "" {
		agentLogLevel = mdb.GetAgentLogLevel()
	}

	agentLogFile := automationconfig.DefaultAgentLogFile
	if mdb.GetAgentLogFile() != "" {
		agentLogFile = mdb.GetAgentLogFile()
	}

	agentMaxLogFileDurationHours := automationconfig.DefaultAgentMaxLogFileDurationHours
	if mdb.GetAgentMaxLogFileDurationHours() != 0 {
		agentMaxLogFileDurationHours = mdb.GetAgentMaxLogFileDurationHours()
	}

	return statefulset.Apply(
		statefulset.WithName(mdb.GetName()),
		statefulset.WithNamespace(mdb.GetNamespace()),
		statefulset.WithServiceName(mdb.ServiceName()),
		statefulset.WithLabels(labels),
		statefulset.WithMatchLabels(labels),
		statefulset.WithReplicas(scale.ReplicasThisReconciliation(scaler)),
		statefulset.WithUpdateStrategyType(mdb.GetUpdateStrategyType()),
		dataVolumeClaim,
		logVolumeClaim,
		singleModeVolumeClaim,
		shareProcessNs,
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podSecurityContext,
				podtemplatespec.WithPodLabels(labels),
				podtemplatespec.WithVolume(healthStatusVolume),
				automationConfigVolumeFunc,
				hooksVolumeMod,
				scriptsVolumeMod,
				podtemplatespec.WithVolume(tmpVolume),
				podtemplatespec.WithVolume(keyFileVolume),
				podtemplatespec.WithServiceAccount(mongodbDatabaseServiceAccountName),
				podtemplatespec.WithContainer(AgentName, mongodbAgentContainer(mdb.AutomationConfigSecretName(), mongodbAgentVolumeMounts, agentLogLevel, agentLogFile, agentMaxLogFileDurationHours, agentImage)),
				podtemplatespec.WithContainer(MongodbName, mongodbContainer(mongodbImage, mongodVolumeMounts, mdb.GetMongodConfiguration(), !withInitContainers)),
				withStaticContainerModification,
				upgradeInitContainer,
				readinessInitContainer,
			),
		))
}

func BaseAgentCommand() string {
	return "agent/mongodb-agent -healthCheckFilePath=" + agentHealthStatusFilePathValue + " -serveStatusPort=5000"
}

// AutomationAgentCommand withAgentAPIKeyExport detects whether we want to deploy this agent with the agent api key exported
// it can be used to register the agent with OM.
func AutomationAgentCommand(withStatic bool, withAgentAPIKeyExport bool, logLevel mdbv1.LogLevel, logFile string, maxLogFileDurationHours int) []string {
	// This is somewhat undocumented at https://www.mongodb.com/docs/ops-manager/current/reference/mongodb-agent-settings/
	// Not setting the -logFile option make the mongodb-agent log to stdout. Setting -logFile /dev/stdout will result in
	// an error by the agent trying to open /dev/stdout-verbose and still trying to do log rotation.
	// To keep consistent with old behavior not setting the logFile in the config does not log to stdout but keeps
	// the default logFile as defined by DefaultAgentLogFile. Setting the logFile explictly to "/dev/stdout" will log to stdout.
	agentLogOptions := ""
	if logFile == "/dev/stdout" {
		agentLogOptions += " -logLevel " + string(logLevel)
	} else {
		agentLogOptions += " -logFile " + logFile + " -logLevel " + string(logLevel) + " -maxLogFileDurationHrs " + strconv.Itoa(maxLogFileDurationHours)
	}

	if withAgentAPIKeyExport {
		return []string{"/bin/bash", "-c", GetMongodbUserCommandWithAPIKeyExport(withStatic) + BaseAgentCommand() + " -cluster=" + clusterFilePath + automationAgentOptions + agentLogOptions}
	}
	return []string{"/bin/bash", "-c", MongodbUserCommand + BaseAgentCommand() + " -cluster=" + clusterFilePath + automationAgentOptions + agentLogOptions}
}

func GetMongodbUserCommandWithAPIKeyExport(withStatic bool) string {
	agentPrepareScript := ""
	if withStatic {
		agentPrepareScript = "/usr/local/bin/setup-agent-files.sh\n"
	}

	//nolint:gosec //The credentials path is hardcoded in the container.
	return fmt.Sprintf(`%scurrent_uid=$(id -u)
AGENT_API_KEY="$(cat /mongodb-automation/agent-api-key/agentApiKey)"
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
echo "mongodb:x:$(id -u):$(id -g):,,,:/:/bin/bash" >> /tmp/passwd
export NSS_WRAPPER_PASSWD=/tmp/passwd
export LD_PRELOAD=libnss_wrapper.so
export NSS_WRAPPER_GROUP=/etc/group
fi
`, agentPrepareScript)
}

func mongodbAgentContainer(automationConfigSecretName string, volumeMounts []corev1.VolumeMount, logLevel mdbv1.LogLevel, logFile string, maxLogFileDurationHours int, agentImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(AgentName),
		container.WithImage(agentImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithReadinessProbe(DefaultReadiness()),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand(AutomationAgentCommand(false, false, logLevel, logFile, maxLogFileDurationHours)),
		containerSecurityContext,
		container.WithEnvs(
			corev1.EnvVar{
				Name:  headlessAgentEnv,
				Value: "true",
			},
			corev1.EnvVar{
				Name: podNamespaceEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.namespace",
					},
				},
			},
			corev1.EnvVar{
				Name:  automationConfigEnv,
				Value: automationConfigSecretName,
			},
			corev1.EnvVar{
				Name:  agentHealthStatusFilePathEnv,
				Value: agentHealthStatusFilePathValue,
			},
		),
	)
}

func mongodbAgentUtilitiesContainer(volumeMounts []corev1.VolumeMount, initDatabaseImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(util.AgentContainerUtilitiesName),
		container.WithImage(initDatabaseImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand([]string{"bash", "-c", "touch /tmp/agent-utilities-holder_marker && tail -F -n0 /tmp/agent-utilities-holder_marker"}),
		container.WithArgs([]string{""}),
		containerSecurityContext,
	)
}

func versionUpgradeHookInit(volumeMount []corev1.VolumeMount, versionUpgradeHookImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(versionUpgradeHookName),
		container.WithCommand([]string{"cp", "version-upgrade-hook", "/hooks/version-upgrade"}),
		container.WithImage(versionUpgradeHookImage),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithVolumeMounts(volumeMount),
		containerSecurityContext,
	)
}

func DefaultReadiness() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{readinessProbePath}),
		probes.WithFailureThreshold(40),
		probes.WithInitialDelaySeconds(5),
	)
}

func dataPvc(dataVolumeName string) persistentvolumeclaim.Modification {
	return persistentvolumeclaim.Apply(
		persistentvolumeclaim.WithName(dataVolumeName),
		persistentvolumeclaim.WithAccessModes(corev1.ReadWriteOnce),
		persistentvolumeclaim.WithResourceRequests(resourcerequirements.BuildDefaultStorageRequirements()),
	)
}

func logsPvc(logsVolumeName string) persistentvolumeclaim.Modification {
	return persistentvolumeclaim.Apply(
		persistentvolumeclaim.WithName(logsVolumeName),
		persistentvolumeclaim.WithAccessModes(corev1.ReadWriteOnce),
		persistentvolumeclaim.WithResourceRequests(resourcerequirements.BuildStorageRequirements("2G")),
	)
}

// readinessProbeInit returns a modification function which will add the readiness probe container.
// this container will copy the readiness probe binary into the /opt/scripts directory.
func readinessProbeInit(volumeMount []corev1.VolumeMount, readinessProbeImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(ReadinessProbeContainerName),
		container.WithCommand([]string{"cp", "/probes/readinessprobe", "/opt/scripts/readinessprobe"}),
		container.WithImage(readinessProbeImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithVolumeMounts(volumeMount),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		containerSecurityContext,
	)
}

// buildSignalHandling returns the signal handling setup for static architecture
func buildSignalHandling() string {
	return fmt.Sprintf(`
# Signal handler for graceful shutdown in shared PID namespace
cleanup() {
	# Important! Keep this in sync with DefaultPodTerminationPeriodSeconds constant from constants.go
	termination_timeout_seconds=%d

	echo "MongoDB container received SIGTERM, shutting down gracefully..."

	if [ -n "$MONGOD_PID" ] && kill -0 "$MONGOD_PID" 2>/dev/null; then
		echo "Sending SIGTERM to mongod process $MONGOD_PID"
		kill -15 "$MONGOD_PID"

		echo "Waiting until mongod process is shutdown. Note, that if mongod process fails to shutdown in the time specified by the 'terminationGracePeriodSeconds' property (default ${termination_timeout_seconds} seconds) then the container will be killed by Kubernetes."

		# Use the same robust waiting mechanism as agent-launcher-lib.sh
		# We cannot use 'wait' for processes started in background, use spinning loop
		while [ -e "/proc/${MONGOD_PID}" ]; do
			sleep 0.1
		done

		echo "mongod process has exited"
	fi

	echo "MongoDB container shutdown complete"
	exit 0
}

# Set up signal handler for static architecture
trap cleanup SIGTERM
`, util.DefaultPodTerminationPeriodSeconds)
}

// buildMongodExecution returns the mongod execution command based on architecture
// in static we run /pause as pid1 and we need to ensure to redirect sigterm to the mongod process
func buildMongodExecution(filePath string, isStatic bool) string {
	if isStatic {
		return fmt.Sprintf(`mongod -f %s &
MONGOD_PID=$!
echo "Started mongod with PID $MONGOD_PID"

# Wait for mongod to finish
wait "$MONGOD_PID"`, filePath)
	}
	return fmt.Sprintf("exec mongod -f %s", filePath)
}

// buildMongodbCommand constructs the complete MongoDB container command
func buildMongodbCommand(filePath string, isStatic bool) string {
	signalHandling := ""
	if isStatic {
		signalHandling = buildSignalHandling()
	}

	mongodExec := buildMongodExecution(filePath, isStatic)

	return fmt.Sprintf(`%s
if [ -e "/hooks/version-upgrade" ]; then
	#run post-start hook to handle version changes (if exists)
    /hooks/version-upgrade
fi

# wait for config and keyfile to be created by the agent
echo "Waiting for config and keyfile files to be created by the agent..."
while ! [ -f %s -a -f %s ]; do
	sleep 3;
	echo "Waiting..."
done

# sleep is important after agent issues shutdown command
# k8s restarts the mongod container too quickly for the agent to realize mongod is down
echo "Sleeping for 15s..."
sleep 15

# start mongod with this configuration
echo "Starting mongod..."
%s
`, signalHandling, filePath, keyfileFilePath, mongodExec)
}

func mongodbContainer(mongodbImage string, volumeMounts []corev1.VolumeMount, additionalMongoDBConfig mdbv1.MongodConfiguration, isStatic bool) container.Modification {
	filePath := additionalMongoDBConfig.GetDBDataDir() + "/" + automationMongodConfFileName
	mongoDbCommand := buildMongodbCommand(filePath, isStatic)

	containerCommand := []string{
		"/bin/sh",
		"-c",
		mongoDbCommand,
	}

	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	return container.Apply(
		container.WithName(MongodbName),
		container.WithImage(mongodbImage),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithCommand(containerCommand),
		// The official image provides both CMD and ENTRYPOINT. We're reusing the former and need to replace
		// the latter with an empty string.
		container.WithArgs([]string{""}),
		containerSecurityContext,
		container.WithEnvs(
			collectEnvVars()...,
		),
		container.WithVolumeMounts(volumeMounts),
	)
}

// Function to collect and return the environment variables to be used in the
// MongoDB container.
func collectEnvVars() []corev1.EnvVar {
	var envVars []corev1.EnvVar

	envVars = append(envVars, corev1.EnvVar{
		Name:  agentHealthStatusFilePathEnv,
		Value: "/healthstatus/agent-health-status.json",
	})

	addEnvVarIfSet := func(name string) {
		value := os.Getenv(name) // nolint:forbidigo
		if value != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  name,
				Value: value,
			})
		}
	}

	addEnvVarIfSet(config.ReadinessProbeLoggerBackups)
	addEnvVarIfSet(config.ReadinessProbeLoggerMaxSize)
	addEnvVarIfSet(config.ReadinessProbeLoggerMaxAge)
	addEnvVarIfSet(config.ReadinessProbeLoggerCompress)
	addEnvVarIfSet(config.WithAgentFileLogging)

	return envVars
}

// buildVolumesForMembers creates volume configurations for regular MongoDB data members
// These members need persistent storage for data and logs
func buildVolumesForMembers(mdb MongoDBStatefulSetOwner, dataVolumeClaim *statefulset.Modification, logVolumeClaim *statefulset.Modification, singleModeVolumeClaim *func(s *appsv1.StatefulSet), mongodbAgentVolumeMounts *[]corev1.VolumeMount, mongodVolumeMounts *[]corev1.VolumeMount) {

	if mdb.HasSeparateDataAndLogsVolumes() {
		logVolumeMount := statefulset.CreateVolumeMount(mdb.LogsVolumeName(), automationconfig.DefaultAgentLogPath)
		dataVolumeMount := statefulset.CreateVolumeMount(mdb.DataVolumeName(), mdb.GetMongodConfiguration().GetDBDataDir())
		*dataVolumeClaim = statefulset.WithVolumeClaim(mdb.DataVolumeName(), dataPvc(mdb.DataVolumeName()))
		*logVolumeClaim = statefulset.WithVolumeClaim(mdb.LogsVolumeName(), logsPvc(mdb.LogsVolumeName()))
		*mongodbAgentVolumeMounts = append(*mongodbAgentVolumeMounts, dataVolumeMount, logVolumeMount)
		*mongodVolumeMounts = append(*mongodVolumeMounts, dataVolumeMount, logVolumeMount)
	} else {
		mounts := []corev1.VolumeMount{
			statefulset.CreateVolumeMount(mdb.DataVolumeName(), mdb.GetMongodConfiguration().GetDBDataDir(), statefulset.WithSubPath("data")),
			statefulset.CreateVolumeMount(mdb.DataVolumeName(), automationconfig.DefaultAgentLogPath, statefulset.WithSubPath("logs")),
		}
		*mongodbAgentVolumeMounts = append(*mongodbAgentVolumeMounts, mounts...)
		*mongodVolumeMounts = append(*mongodVolumeMounts, mounts...)

	}
}