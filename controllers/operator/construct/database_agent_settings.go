package construct

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func defaultAgentParameters() mdbv1.StartupParameters {
	return map[string]string{"logFile": path.Join(util.PvcMountPathLogs, "automation-agent.log")}
}

// agentRuntime holds the agent container environment values that shape the settings file. The
// operator never sets MMS_HOME or MMS_LOG_DIR, so they are only known from the pod template env
// after merging, which is why they are read from the container and not from the options.
type agentRuntime struct {
	home         string
	logDirectory string
	downloadBase string
	static       bool
}

func newAgentRuntime(variables map[string]string) agentRuntime {
	return agentRuntime{
		home:         cmp.Or(variables["MMS_HOME"], "/mongodb-automation"),
		logDirectory: cmp.Or(variables["MMS_LOG_DIR"], util.PvcMountPathLogs),
		downloadBase: variables[DownloadBaseEnv],
		static:       variables["MDB_STATIC_CONTAINERS_ARCHITECTURE"] != "",
	}
}

func databaseAgentSettings(opts DatabaseStatefulSetOptions, runtime agentRuntime) mdbv1.StartupParameters {
	return resolveAgentSettings(databaseAgentBaseSettings(opts, runtime), opts.AgentConfig)
}

func databaseAgentBaseSettings(opts DatabaseStatefulSetOptions, runtime agentRuntime) mdbv1.StartupParameters {
	settings := defaultAgentParameters()
	podVars := env.PodEnvVars{}
	if opts.PodVars != nil {
		podVars = *opts.PodVars
	}
	settings["mmsGroupId"] = podVars.ProjectID
	settings["pidfilepath"] = path.Join(runtime.home, "mongodb-mms-automation-agent.pid")
	settings["maxLogFileDurationHrs"] = "24"
	settings["logLevel"] = cmp.Or(podVars.LogLevel, "INFO")
	settings["healthCheckFilePath"] = path.Join(runtime.logDirectory, "agent-health-status.json")
	settings["tlsRequireValidMMSServerCertificates"] = strconv.FormatBool(podVars.SSLRequireValidMMSServerCertificates)
	baseURL := strings.TrimSuffix(podVars.BaseURL, "/")
	if baseURL != "" {
		settings["mmsBaseUrl"] = baseURL
	} else {
		settings["cluster"] = "/var/lib/mongodb-automation/cluster-config.json"
		settings["serveStatusPort"] = "5000"
	}
	if runtime.static {
		settings["operatorMode"] = strconv.FormatBool(true)
		settings["binariesFixedPath"] = path.Join(cmp.Or(runtime.downloadBase, opts.GetDownloadBase()), "mongod/bin")
	} else {
		settings["useLocalMongoDbTools"] = strconv.FormatBool(true)
	}
	if opts.PodVars != nil && opts.PodVars.SSLMMSCAConfigMap != "" {
		settings["httpsCAFile"] = path.Join(caCertMountPath, util.CaCertMMS)
	}
	if opts.MultiClusterMode || (opts.ExternalAccessConfiguration != nil && opts.ExternalAccessConfiguration.ExternalDomain != nil) {
		settings["ephemeralPortOffset"] = "1"
	}
	return settings
}

// AgentSettingsOverrides drops the optional leading dash from customer startupOptions keys, so
// "-logLevel" keeps working as it did when the launcher built command line flags. Keys are applied
// in sorted order, so "logLevel" wins over "-logLevel" deterministically.
func AgentSettingsOverrides(parameters mdbv1.StartupParameters) mdbv1.StartupParameters {
	overrides := make(mdbv1.StartupParameters, len(parameters))
	for _, key := range slices.Sorted(maps.Keys(parameters)) {
		overrides[strings.TrimPrefix(key, "-")] = parameters[key]
	}
	return overrides
}

// resolveAgentSettings returns base with the customer startupOptions applied and the credential
// delivery settled. The credential step has to run after the merge because it depends on which
// options the customer set.
func resolveAgentSettings(base mdbv1.StartupParameters, agentConfig *mdbv1.AgentConfig) mdbv1.StartupParameters {
	settings := maps.Clone(base)
	hasAPIKeyOverride := false
	if agentConfig != nil {
		// Mode selection in databaseAgentBaseSettings runs on the environment, before this merge.
		// An empty mmsBaseUrl must not leave the settings file with neither mmsBaseUrl nor the
		// headless cluster/serveStatusPort keys, because the agent would have no usable mode.
		for key, value := range AgentSettingsOverrides(agentConfig.StartupParameters) {
			if ignoredEmptyAgentOverride(key, value) {
				continue
			}
			settings[key] = value
			hasAPIKeyOverride = hasAPIKeyOverride || key == agentAPIKeySetting
		}
	}
	delete(settings, agentAPIKeySetting)
	delete(settings, "overrideLocalHost")
	// A customer command wins over the generated one. Empty values never reach this map, so
	// an unset mmsApiKeyExec is simply absent.
	if settings[agentAPIKeyExecSetting] == "" && (settings["mmsBaseUrl"] != "" || hasAPIKeyOverride) {
		// Both the generated key Secret and the override Secret mount at AgentAPIKeySecretPath.
		// MMS_HOME controls the agent's pidfile, not the credential mount.
		settings[agentAPIKeyExecSetting] = agentAPIKeyExecCommand()
	}
	return settings
}

func agentAPIKeyExecCommand() string {
	keyPath := path.Join(AgentAPIKeySecretPath, util.OmAgentApiKey)
	return "cat '" + strings.ReplaceAll(keyPath, "'", "'\\''") + "'"
}

// AgentSettingsConfigMapName returns the name of the ConfigMap that holds the agent settings file.
func AgentSettingsConfigMapName(sts appsv1.StatefulSet) string {
	return sts.Name + "-agent-settings"
}

// AgentAPIKeySecretName returns the Secret that provides the agent API key. It may be the
// operator-generated key Secret or a customer override Secret.
func AgentAPIKeySecretName(sts appsv1.StatefulSet) string {
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name == AgentAPIKeyVolumeName && volume.Secret != nil {
			return volume.Secret.SecretName
		}
	}
	return ""
}

// DatabaseAgentSettingsConfigMap renders shared settings from the effective agent environment and customer startup options.
func DatabaseAgentSettingsConfigMap(sts appsv1.StatefulSet, opts DatabaseStatefulSetOptions) (corev1.ConfigMap, error) {
	agent := AgentContainer(sts)
	if agent == nil {
		return corev1.ConfigMap{}, fmt.Errorf("agent container not found in StatefulSet %s", sts.Name)
	}
	if err := ValidateNoRuntimeAgentSettingsEnv(*agent); err != nil {
		return corev1.ConfigMap{}, err
	}
	return agentSettingsConfigMap(sts, databaseAgentSettings(opts, newAgentRuntime(env.ToMap(agent.Env...))))
}

// AgentEnvironmentIsRuntime includes kubelet expansion and EnvFrom values not shadowed by an explicit Env entry.
func AgentEnvironmentIsRuntime(agent corev1.Container, name string) bool {
	for _, variable := range agent.Env {
		if variable.Name == name {
			return variable.ValueFrom != nil || strings.Contains(variable.Value, "$(")
		}
	}
	return len(agent.EnvFrom) > 0
}

// agentSettingsEnvironmentVariables lists the environment variables that drive the generated
// settings file. The operator sets each one as a literal, so a customer valueFrom or $(...)
// reference would make the value unknown at reconcile time.
var agentSettingsEnvironmentVariables = map[string]struct{}{
	util.EnvVarBaseUrl:                        {},
	util.EnvVarProjectId:                      {},
	util.EnvVarLogLevel:                       {},
	util.EnvVarMultiClusterMode:               {},
	util.EnvVarSSLRequireValidMMSCertificates: {},
	util.EnvVarSSLTrustedMMSServerCertificate: {},
	"MMS_HOME":                           {},
	"MMS_LOG_DIR":                        {},
	DownloadBaseEnv:                      {},
	"MDB_STATIC_CONTAINERS_ARCHITECTURE": {},
}

// ValidateNoRuntimeAgentSettingsEnv rejects a runtime reference for an environment variable
// that drives the settings file. The file is rendered at reconcile time, so the operator must
// know the value. Set these through agent.startupOptions instead.
func ValidateNoRuntimeAgentSettingsEnv(agent corev1.Container) error {
	for _, variable := range agent.Env {
		if _, ok := agentSettingsEnvironmentVariables[variable.Name]; !ok {
			continue
		}
		if variable.ValueFrom != nil || strings.Contains(variable.Value, "$(") {
			return fmt.Errorf("agent environment variable %s uses a runtime reference, which is not supported, set agent settings through agent.startupOptions", variable.Name)
		}
	}
	return nil
}

func agentSettingsConfigMap(sts appsv1.StatefulSet, settings mdbv1.StartupParameters) (corev1.ConfigMap, error) {
	content, err := settings.ToSettingsFile()
	if err != nil {
		return corev1.ConfigMap{}, err
	}
	return corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            AgentSettingsConfigMapName(sts),
			Namespace:       sts.Namespace,
			Labels:          maps.Clone(sts.Labels),
			OwnerReferences: append([]metav1.OwnerReference(nil), sts.OwnerReferences...),
		},
		Data: map[string]string{AgentSettingsFileName: content},
	}, nil
}

// ignoredEmptyAgentOverride reports a customer option that cannot mean "set this to empty",
// because the agent would lose its connection mode or credential.
func ignoredEmptyAgentOverride(key, value string) bool {
	return value == "" && (key == "mmsBaseUrl" || key == agentAPIKeyExecSetting)
}
