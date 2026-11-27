package construct

import (
	"cmp"
	"crypto/sha256"
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
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
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
		home:         cmp.Or(variables[MmsHomeEnv], util.PvcMmsHomeMountPath),
		logDirectory: cmp.Or(variables[MmsLogDirEnv], util.PvcMountPathLogs),
		downloadBase: variables[DownloadBaseEnv],
		static:       variables[StaticContainersArchitectureEnv] != "",
	}
}

func databaseAgentSettings(opts DatabaseStatefulSetOptions, runtime agentRuntime) mdbv1.StartupParameters {
	return resolveAgentSettings(databaseAgentBaseSettings(opts, runtime), opts.AgentSettingsSource())
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
func resolveAgentSettings(base mdbv1.StartupParameters, source AgentSettingsSource) mdbv1.StartupParameters {
	settings := maps.Clone(base)
	hasAPIKeyOverride := false
	// Mode selection in databaseAgentBaseSettings runs on the environment, before this merge.
	// An empty mmsBaseUrl must not leave the settings file with neither mmsBaseUrl nor the
	// headless cluster/serveStatusPort keys, because the agent would have no usable mode.
	for key, value := range source.startupParameters() {
		if value == "" && (key == "mmsBaseUrl" || key == "mmsApiKeyExec" || key == "mmsApiKey") {
			continue
		}
		settings[key] = value
		hasAPIKeyOverride = hasAPIKeyOverride || key == "mmsApiKey"
	}
	delete(settings, "mmsApiKey")
	delete(settings, "overrideLocalHost")
	// A customer command wins over the generated one. Empty values never reach this map, so
	// an unset mmsApiKeyExec is simply absent.
	if settings["mmsApiKeyExec"] == "" && (settings["mmsBaseUrl"] != "" || hasAPIKeyOverride) {
		// Both the generated key Secret and the override Secret mount at AgentAPIKeySecretPath.
		// MMS_HOME controls the agent's pidfile, not the credential mount.
		settings["mmsApiKeyExec"] = agentAPIKeyExecCommand()
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

// MountedAgentAPIKeySecretName returns the Secret mounted as the agent API key volume. It may be the
// operator-generated key Secret or a customer override Secret.
func MountedAgentAPIKeySecretName(sts appsv1.StatefulSet) string {
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name == AgentAPIKeyVolumeName && volume.Secret != nil {
			return volume.Secret.SecretName
		}
	}
	return ""
}

// DatabaseAgentSettingsConfigMap renders shared settings from the effective agent environment and customer startup options.
func DatabaseAgentSettingsConfigMap(sts appsv1.StatefulSet, agent corev1.Container, opts DatabaseStatefulSetOptions) (corev1.ConfigMap, error) {
	if err := ValidateNoRuntimeAgentSettingsEnv(agent); err != nil {
		return corev1.ConfigMap{}, err
	}
	return agentSettingsConfigMap(sts, databaseAgentSettings(opts, newAgentRuntime(env.ToMap(agent.Env...))))
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
	MmsHomeEnv:                      {},
	MmsLogDirEnv:                    {},
	DownloadBaseEnv:                 {},
	StaticContainersArchitectureEnv: {},
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
		Data: map[string]string{util.AgentSettingsFileName: content},
	}, nil
}

const agentHostnameOverrideEnv = "MDB_AGENT_HOSTNAME_OVERRIDE"

// applyAgentSettings finishes the agent part of the pod template: the settings mount, the override
// key volume, the hostname env and the rollout hash. DatabaseStatefulSetHelper runs it after the
// customer pod template merge because the settings are rendered from the merged agent environment.
func applyAgentSettings(sts *appsv1.StatefulSet, opts DatabaseStatefulSetOptions) {
	agent := AgentContainer(*sts)
	if agent == nil {
		return
	}
	if _, ok := opts.AgentSettingsSource().APIKeyOverride(); ok && !vault.IsVaultSecretBackend() {
		upsertAgentAPIKeyVolume(sts, agent, AgentAPIKeyOverrideSecretName(sts.Name))
	}
	podtemplatespec.WithVolume(statefulset.CreateVolumeFromConfigMap(util.AgentSettingsVolume, AgentSettingsConfigMapName(*sts)))(&sts.Spec.Template)
	container.WithVolumeMounts([]corev1.VolumeMount{statefulset.CreateVolumeMount(util.AgentSettingsVolume, util.AgentSettingsPath, statefulset.WithReadOnly(true))})(agent)
	setAgentHostnameEnv(agent, opts)
	cm, err := DatabaseAgentSettingsConfigMap(*sts, *agent, opts)
	if err != nil {
		// DatabaseInKubernetes renders the settings again and reports the error before writing anything.
		return
	}
	digest := sha256.Sum256([]byte(cm.Data[util.AgentSettingsFileName] + "\x00" + opts.AgentCredentialHash))
	sts.Spec.Template.Annotations = merge.StringToStringMap(sts.Spec.Template.Annotations, map[string]string{util.AgentSettingsHash: fmt.Sprintf("%x", digest)})
}

// AgentSettingsSource is the agent config and StatefulSet override a StatefulSet's options are built from.
type AgentSettingsSource struct {
	Config   *mdbv1.AgentConfig
	Override *appsv1.StatefulSetSpec
}

// startupParameters returns the customer startup options with the optional leading dash dropped.
func (s AgentSettingsSource) startupParameters() mdbv1.StartupParameters {
	if s.Config == nil {
		return nil
	}
	return AgentSettingsOverrides(s.Config.StartupParameters)
}

// CustomerAPIKeyExec returns the customer's mmsApiKeyExec, or "" when they set none.
func (s AgentSettingsSource) CustomerAPIKeyExec() string {
	return s.startupParameters()["mmsApiKeyExec"]
}

// APIKeyOverride returns the customer's raw mmsApiKey, unless their own mmsApiKeyExec reads the key.
func (s AgentSettingsSource) APIKeyOverride() (string, bool) {
	if s.CustomerAPIKeyExec() != "" {
		return "", false
	}
	key, ok := s.startupParameters()["mmsApiKey"]
	return key, ok && key != ""
}

// ReadsCredential reports whether the agent must have the API key to start, judged from the
// customer's mmsBaseUrl override or else the configured base URL.
func (s AgentSettingsSource) ReadsCredential(baseURL string) bool {
	if override, ok := s.startupParameters()["mmsBaseUrl"]; ok {
		return override != ""
	}
	return baseURL != ""
}

// AgentAPIKeyOverrideSecretName returns the generated Secret that holds the customer's raw mmsApiKey.
func AgentAPIKeyOverrideSecretName(stsName string) string {
	return stsName + "-agent-api-key"
}

// ReadsMountedAPIKey reports whether the settings file makes the agent read the key from the
// mounted Secret. It depends only on the customer startup options and the base URL.
func (s AgentSettingsSource) ReadsMountedAPIKey(baseURL string) bool {
	base := mdbv1.StartupParameters{}
	if trimmed := strings.TrimSuffix(baseURL, "/"); trimmed != "" {
		base["mmsBaseUrl"] = trimmed
	}
	return resolveAgentSettings(base, s)["mmsApiKeyExec"] == agentAPIKeyExecCommand()
}

// setAgentHostnameEnv passes a customer overrideLocalHost to the launcher. It is appended last
// because Kubernetes expands references using the entries that precede them.
func setAgentHostnameEnv(agent *corev1.Container, opts DatabaseStatefulSetOptions) {
	agent.Env = slices.DeleteFunc(agent.Env, func(variable corev1.EnvVar) bool { return variable.Name == agentHostnameOverrideEnv })
	if hostname, ok := opts.AgentSettingsSource().startupParameters()["overrideLocalHost"]; ok {
		agent.Env = append(agent.Env, corev1.EnvVar{Name: agentHostnameOverrideEnv, Value: hostname})
	}
}

// upsertAgentAPIKeyVolume replaces the projected API-key volume in place and keeps the existing
// item mode. WithVolume merges item lists, so it would keep stale keys that the generated Secret
// does not contain. replaceVolume replaces the whole volume instead.
func upsertAgentAPIKeyVolume(sts *appsv1.StatefulSet, agent *corev1.Container, secretName string) {
	keyVolume := statefulset.CreateVolumeFromSecret(AgentAPIKeyVolumeName, secretName)
	for _, volume := range sts.Spec.Template.Spec.Volumes {
		if volume.Name != AgentAPIKeyVolumeName || volume.Secret == nil {
			continue
		}
		keyVolume = *volume.DeepCopy()
		keyVolume.Secret.SecretName = secretName
		if len(volume.Secret.Items) > 0 {
			// The generated Secret contains only the override credential.
			item := corev1.KeyToPath{Key: util.OmAgentApiKey, Path: util.OmAgentApiKey}
			for _, existing := range volume.Secret.Items {
				if existing.Path == item.Path {
					item.Mode = existing.Mode
					break
				}
			}
			keyVolume.Secret.Items = []corev1.KeyToPath{item}
		}
		break
	}
	replaceVolume(&sts.Spec.Template, keyVolume)
	if !slices.ContainsFunc(agent.VolumeMounts, func(mount corev1.VolumeMount) bool { return mount.Name == AgentAPIKeyVolumeName }) {
		container.WithVolumeMounts([]corev1.VolumeMount{statefulset.CreateVolumeMount(AgentAPIKeyVolumeName, AgentAPIKeySecretPath, statefulset.WithReadOnly(true))})(agent)
	}
}

// replaceVolume replaces a volume by name without changing its position, or appends it.
func replaceVolume(template *corev1.PodTemplateSpec, volume corev1.Volume) {
	for i := range template.Spec.Volumes {
		if template.Spec.Volumes[i].Name == volume.Name {
			template.Spec.Volumes[i] = volume
			return
		}
	}
	template.Spec.Volumes = append(template.Spec.Volumes, volume)
}
