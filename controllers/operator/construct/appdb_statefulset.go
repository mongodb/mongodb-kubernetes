package construct

import (
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/persistentvolumeclaim"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/resourcerequirements"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	appdbVersionUpgradeHookName       = "mongod-posthook"
	AppDBReadinessProbeContainerName  = "mongodb-agent-readinessprobe"
	appdbReadinessProbePath           = "/opt/scripts/readinessprobe"
	appdbAutomationMongodConfFileName = "automation-mongod.conf"
	appdbKeyfileFilePath              = "/var/lib/mongodb-mms-automation/authentication/keyfile"

	appdbHeadlessAgentEnv    = "HEADLESS_AGENT"
	appdbPodNamespaceEnv     = "POD_NAMESPACE"
	appdbAutomationConfigEnv = "AUTOMATION_CONFIG_MAP"
)

func appdbMongodbAgentContainer(automationConfigSecretName string, volumeMounts []corev1.VolumeMount, agentImage string, command []string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(util.AgentContainerName),
		container.WithImage(agentImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithReadinessProbe(appdbDefaultReadiness()),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand(command),
		containerSecurityContext,
		container.WithEnvs(
			corev1.EnvVar{
				Name:  appdbHeadlessAgentEnv,
				Value: "true",
			},
			corev1.EnvVar{
				Name: appdbPodNamespaceEnv,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.namespace",
					},
				},
			},
			corev1.EnvVar{
				Name:  appdbAutomationConfigEnv,
				Value: automationConfigSecretName,
			},
			corev1.EnvVar{
				Name:  appdbAgentHealthStatusFilePathEnv,
				Value: appdbAgentHealthStatusFilePathValue,
			},
		),
	)
}

func appdbMongodbAgentUtilitiesContainer(volumeMounts []corev1.VolumeMount, initDatabaseImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(util.AgentContainerUtilitiesName),
		container.WithImage(initDatabaseImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand([]string{"bash", "-c", "touch /tmp/agent-utilities-holder_marker && exec -a agent-utilities-holder_marker tail -f /dev/null"}),
		container.WithArgs([]string{""}),
		containerSecurityContext,
	)
}

func appdbVersionUpgradeHookInit(volumeMount []corev1.VolumeMount, versionUpgradeHookImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(appdbVersionUpgradeHookName),
		container.WithCommand([]string{"cp", "version-upgrade-hook", "/hooks/version-upgrade"}),
		container.WithImage(versionUpgradeHookImage),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithVolumeMounts(volumeMount),
		containerSecurityContext,
	)
}

func appdbDefaultReadiness() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{appdbReadinessProbePath}),
		probes.WithFailureThreshold(40),
		probes.WithInitialDelaySeconds(5),
	)
}

func appdbDataPvc(dataVolumeName string) persistentvolumeclaim.Modification {
	return persistentvolumeclaim.Apply(
		persistentvolumeclaim.WithName(dataVolumeName),
		persistentvolumeclaim.WithAccessModes(corev1.ReadWriteOnce),
		persistentvolumeclaim.WithResourceRequests(resourcerequirements.BuildDefaultStorageRequirements()),
	)
}

func appdbLogsPvc(logsVolumeName string) persistentvolumeclaim.Modification {
	return persistentvolumeclaim.Apply(
		persistentvolumeclaim.WithName(logsVolumeName),
		persistentvolumeclaim.WithAccessModes(corev1.ReadWriteOnce),
		persistentvolumeclaim.WithResourceRequests(resourcerequirements.BuildStorageRequirements("2G")),
	)
}

// appdbReadinessProbeInit returns a modification function which will add the readiness probe container.
// this container will copy the readiness probe binary into the /opt/scripts directory.
func appdbReadinessProbeInit(volumeMount []corev1.VolumeMount, readinessProbeImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName(AppDBReadinessProbeContainerName),
		container.WithCommand([]string{"cp", "/probes/readinessprobe", "/opt/scripts/readinessprobe"}),
		container.WithImage(readinessProbeImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithVolumeMounts(volumeMount),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		containerSecurityContext,
	)
}

// appdbBuildSignalHandling returns the signal handling setup for static architecture.
func appdbBuildSignalHandling() string {
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

// appdbBuildMongodExecution returns the mongod execution command based on architecture.
// In static we run /pause as pid1 and we need to ensure to redirect sigterm to the mongod process.
func appdbBuildMongodExecution(filePath string, isStatic bool) string {
	if isStatic {
		return fmt.Sprintf(`mongod -f %s &
MONGOD_PID=$!
echo "Started mongod with PID $MONGOD_PID"

# Wait for mongod to finish
wait "$MONGOD_PID"`, filePath)
	}
	return fmt.Sprintf("exec mongod -f %s", filePath)
}

// appdbBuildMongodbCommand constructs the complete MongoDB container command.
func appdbBuildMongodbCommand(filePath string, isStatic bool) string {
	signalHandling := ""
	if isStatic {
		signalHandling = appdbBuildSignalHandling()
	}

	mongodExec := appdbBuildMongodExecution(filePath, isStatic)

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
`, signalHandling, filePath, appdbKeyfileFilePath, mongodExec)
}

func appdbMongodbContainer(mongodbImage string, volumeMounts []corev1.VolumeMount, additionalMongoDBConfig v1.MongodConfiguration, isStatic bool) container.Modification {
	filePath := additionalMongoDBConfig.GetDBDataDir() + "/" + appdbAutomationMongodConfFileName
	mongoDbCommand := appdbBuildMongodbCommand(filePath, isStatic)

	containerCommand := []string{
		"/bin/sh",
		"-c",
		mongoDbCommand,
	}

	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	return container.Apply(
		container.WithName(util.MongodbContainerName),
		container.WithImage(mongodbImage),
		container.WithResourceRequirements(resourcerequirements.Defaults()),
		container.WithCommand(containerCommand),
		// The official image provides both CMD and ENTRYPOINT. We're reusing the former and need to replace
		// the latter with an empty string.
		container.WithArgs([]string{""}),
		containerSecurityContext,
		container.WithEnvs(
			appdbCollectEnvVars()...,
		),
		container.WithVolumeMounts(volumeMounts),
	)
}

// appdbCollectEnvVars collects and returns the environment variables to be used in the MongoDB container.
func appdbCollectEnvVars() []corev1.EnvVar {
	var envVars []corev1.EnvVar

	envVars = append(envVars, corev1.EnvVar{
		Name:  appdbAgentHealthStatusFilePathEnv,
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

	addEnvVarIfSet(appdbReadinessProbeLoggerBackups)
	addEnvVarIfSet(appdbReadinessProbeLoggerMaxSize)
	addEnvVarIfSet(appdbReadinessProbeLoggerMaxAge)
	addEnvVarIfSet(appdbReadinessProbeLoggerCompress)
	addEnvVarIfSet(appdbWithAgentFileLogging)

	return envVars
}
