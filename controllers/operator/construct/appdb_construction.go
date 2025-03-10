package construct

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/envvar"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

const (
	appDBServiceAccount    = "mongodb-enterprise-appdb"
	InitAppDbContainerName = "mongodb-enterprise-init-appdb"
	// AppDB environment variable names
	InitAppdbVersionEnv          = "INIT_APPDB_VERSION"
	podNamespaceEnv              = "POD_NAMESPACE"
	automationConfigMapEnv       = "AUTOMATION_CONFIG_MAP"
	headlessAgentEnv             = "HEADLESS_AGENT"
	clusterDomainEnv             = "CLUSTER_DOMAIN"
	monitoringAgentContainerName = "mongodb-agent-monitoring"
	// Since the Monitoring Agent is created based on Agent's Pod spec (we modfy it using addMonitoringContainer),
	// We can not reuse "tmp" here - this name is already taken and could lead to a clash. It's better to
	// come up with a unique name here.
	tmpSubpathName = "mongodb-agent-monitoring-tmp"

	monitoringAgentHealthStatusFilePathValue = "/var/log/mongodb-mms-automation/healthstatus/monitoring-agent-health-status.json"
)

type AppDBStatefulSetOptions struct {
	VaultConfig vault.VaultConfiguration
	CertHash    string

	InitAppDBImage             string
	MongodbImage               string
	AgentImage                 string
	LegacyMonitoringAgentImage string

	PrometheusTLSCertHash string
}

func getMonitoringAgentLogOptions(spec om.AppDBSpec) string {
	return fmt.Sprintf(" -logFile=/var/log/mongodb-mms-automation/monitoring-agent.log -maxLogFileDurationHrs=%d -logLevel=%s", spec.GetAgentMaxLogFileDurationHours(), spec.GetAgentLogLevel())
}

// getContainerIndexByName returns the index of a container with the given name in a slice of containers.
// It returns -1 if it doesn't exist
func getContainerIndexByName(containers []corev1.Container, name string) int {
	for i, container := range containers {
		if container.Name == name {
			return i
		}
	}
	return -1
}

// removeContainerByName removes the container with the given name from the input slice, if it exists.
func removeContainerByName(containers []corev1.Container, name string) []corev1.Container {
	index := getContainerIndexByName(containers, name)
	if index == -1 {
		return containers
	}
	return append(containers[:index], containers[index+1:]...)
}

// appDbLabels returns a statefulset modification which adds labels that are specific to the appDB.
func appDbLabels(opsManager *om.MongoDBOpsManager, memberClusterNum int) statefulset.Modification {
	podLabels := map[string]string{
		appLabelKey:             opsManager.Spec.AppDB.HeadlessServiceSelectorAppLabel(memberClusterNum),
		ControllerLabelName:     util.OperatorName,
		PodAntiAffinityLabelKey: opsManager.Spec.AppDB.NameForCluster(memberClusterNum),
	}
	return statefulset.Apply(
		statefulset.WithLabels(opsManager.Labels),
		statefulset.WithMatchLabels(podLabels),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podtemplatespec.WithPodLabels(podLabels),
			),
		),
	)
}

// appDbPodSpec return the podtemplatespec modification required for the AppDB statefulset.
func appDbPodSpec(initContainerImage string, om om.MongoDBOpsManager) podtemplatespec.Modification {
	// The following sets almost the exact same values for the containers
	// But with the addition of a default memory request for the mongod one
	appdbPodSpec := NewDefaultPodSpecWrapper(*om.Spec.AppDB.PodSpec)
	mongoPodSpec := *appdbPodSpec
	mongoPodSpec.Default.MemoryRequests = util.DefaultMemoryAppDB
	mongoPodTemplateFunc := podtemplatespec.WithContainer(
		construct.MongodbName,
		container.WithResourceRequirements(buildRequirementsFromPodSpec(mongoPodSpec)),
	)
	automationPodTemplateFunc := podtemplatespec.WithContainer(
		construct.AgentName,
		container.WithResourceRequirements(buildRequirementsFromPodSpec(*appdbPodSpec)),
	)

	initUpdateFunc := podtemplatespec.NOOP()
	if !architectures.IsRunningStaticArchitecture(om.Annotations) {
		// appdb will have a single init container,
		// all the necessary binaries will be copied into the various
		// volumes of different containers.
		initUpdateFunc = func(templateSpec *corev1.PodTemplateSpec) {
			templateSpec.Spec.InitContainers = []corev1.Container{}
			scriptsVolumeMount := statefulset.CreateVolumeMount("agent-scripts", "/opt/scripts", statefulset.WithReadOnly(false))
			hooksVolumeMount := statefulset.CreateVolumeMount("hooks", "/hooks", statefulset.WithReadOnly(false))
			podtemplatespec.WithInitContainer(InitAppDbContainerName, buildAppDBInitContainer(initContainerImage, []corev1.VolumeMount{scriptsVolumeMount, hooksVolumeMount}))(templateSpec)
		}
	}

	return podtemplatespec.Apply(
		mongoPodTemplateFunc,
		automationPodTemplateFunc,
		initUpdateFunc,
	)
}

// buildAppDBInitContainer builds the container specification for mongodb-enterprise-init-appdb image.
func buildAppDBInitContainer(initContainerImageURL string, volumeMounts []corev1.VolumeMount) container.Modification {
	_, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	return container.Apply(
		container.WithName(InitAppDbContainerName),
		container.WithImage(initContainerImageURL),
		container.WithCommand([]string{"/bin/sh", "-c", `

# the agent requires the readiness probe
cp /probes/readinessprobe /opt/scripts/readinessprobe

# the mongod requires the version upgrade hook
cp /probes/version-upgrade-hook /hooks/version-upgrade
`}),
		container.WithVolumeMounts(volumeMounts),
		configureContainerSecurityContext,
	)
}

// getTLSVolumesAndVolumeMounts returns the slices of volumes and volume-mounts
// that the AppDB STS needs for TLS resources.
func getTLSVolumesAndVolumeMounts(appDb om.AppDBSpec, podVars *env.PodEnvVars, log *zap.SugaredLogger) ([]corev1.Volume, []corev1.VolumeMount) {
	if log == nil {
		log = zap.S()
	}
	var volumesToAdd []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	if ShouldMountSSLMMSCAConfigMap(podVars) {
		// This volume wil contain the OM CA
		caCertVolume := statefulset.CreateVolumeFromConfigMap(CaCertName, podVars.SSLMMSCAConfigMap)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: caCertMountPath,
			Name:      caCertVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, caCertVolume)
	}

	secretName := appDb.GetSecurity().MemberCertificateSecretName(appDb.Name())

	secretName += certs.OperatorGeneratedCertSuffix
	optionalSecretFunc := func(v *corev1.Volume) { v.Secret.Optional = util.BooleanRef(true) }
	optionalConfigMapFunc := func(v *corev1.Volume) { v.ConfigMap.Optional = util.BooleanRef(true) }

	if !vault.IsVaultSecretBackend() {
		secretVolume := statefulset.CreateVolumeFromSecret(util.SecretVolumeName, secretName, optionalSecretFunc)

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: util.SecretVolumeMountPath + "/certs",
			Name:      secretVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, secretVolume)
	}
	caName := CAConfigMapName(appDb, log)

	caVolume := statefulset.CreateVolumeFromConfigMap(tls.ConfigMapVolumeCAName, caName, optionalConfigMapFunc)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		MountPath: util.ConfigMapVolumeCAMountPath,
		Name:      caVolume.Name,
		ReadOnly:  true,
	})
	volumesToAdd = append(volumesToAdd, caVolume)

	prometheusVolumes, prometheusVolumeMounts := getTLSPrometheusVolumeAndVolumeMount(appDb.Prometheus)
	volumesToAdd = append(volumesToAdd, prometheusVolumes...)
	volumeMounts = append(volumeMounts, prometheusVolumeMounts...)

	return volumesToAdd, volumeMounts
}

func CAConfigMapName(appDb om.AppDBSpec, log *zap.SugaredLogger) string {
	caName := fmt.Sprintf("%s-ca", appDb.Name())

	tlsConfig := appDb.GetTLSConfig()
	if tlsConfig.CA != "" {
		caName = tlsConfig.CA
	} else {
		log.Debugf("No CA config map name has been supplied, defaulting to: %s", caName)
	}

	return caName
}

// tlsVolumes returns the podtemplatespec modification that adds all needed volumes
// and volumemounts for TLS.
func tlsVolumes(appDb om.AppDBSpec, podVars *env.PodEnvVars, log *zap.SugaredLogger) podtemplatespec.Modification {
	volumesToAdd, volumeMounts := getTLSVolumesAndVolumeMounts(appDb, podVars, log)
	volumesFunc := func(spec *corev1.PodTemplateSpec) {
		for _, v := range volumesToAdd {
			podtemplatespec.WithVolume(v)(spec)
		}
	}

	return podtemplatespec.Apply(
		volumesFunc,
		podtemplatespec.WithContainer(
			construct.AgentName,
			container.WithVolumeMounts(volumeMounts),
		),
		podtemplatespec.WithContainer(
			construct.MongodbName,
			container.WithVolumeMounts(volumeMounts),
		),
	)
}

func vaultModification(appDB om.AppDBSpec, podVars *env.PodEnvVars, opts AppDBStatefulSetOptions) podtemplatespec.Modification {
	modification := podtemplatespec.NOOP()
	if vault.IsVaultSecretBackend() {
		appDBSecretsToInject := vault.AppDBSecretsToInject{Config: opts.VaultConfig}
		if podVars != nil && podVars.ProjectID != "" {
			appDBSecretsToInject.AgentApiKey = agents.ApiKeySecretName(podVars.ProjectID)
		}
		if appDB.GetSecurity().IsTLSEnabled() {
			secretName := appDB.GetSecurity().MemberCertificateSecretName(appDB.Name()) + certs.OperatorGeneratedCertSuffix
			appDBSecretsToInject.TLSSecretName = secretName
			appDBSecretsToInject.TLSClusterHash = opts.CertHash
		}

		appDBSecretsToInject.AutomationConfigSecretName = appDB.AutomationConfigSecretName()
		appDBSecretsToInject.AutomationConfigPath = util.AppDBAutomationConfigKey
		appDBSecretsToInject.AgentType = "automation-agent"

		if appDB.Prometheus != nil && appDB.Prometheus.TLSSecretRef.Name != "" && opts.PrometheusTLSCertHash != "" {
			appDBSecretsToInject.PrometheusTLSCertHash = opts.PrometheusTLSCertHash
			appDBSecretsToInject.PrometheusTLSPath = fmt.Sprintf("%s%s", appDB.Prometheus.TLSSecretRef.Name, certs.OperatorGeneratedCertSuffix)
		}

		modification = podtemplatespec.Apply(
			modification,
			podtemplatespec.WithAnnotations(appDBSecretsToInject.AppDBAnnotations(appDB.Namespace)),
		)

	} else {
		if ShouldEnableMonitoring(podVars) {
			// AGENT-API-KEY volume
			modification = podtemplatespec.Apply(
				modification,
				podtemplatespec.WithVolume(statefulset.CreateVolumeFromSecret(AgentAPIKeyVolumeName, agents.ApiKeySecretName(podVars.ProjectID))),
			)
		}
	}
	return modification
}

// customPersistenceConfig applies to the statefulset the modifications
// provided by the user through spec.persistence.
func customPersistenceConfig(om *om.MongoDBOpsManager) statefulset.Modification {
	defaultPodSpecPersistence := newDefaultPodSpec().Persistence
	// Two main branches - as the user can either define a single volume for data, logs and journal
	// or three different volumes
	if !om.Spec.AppDB.HasSeparateDataAndLogsVolumes() {
		var config *mdbv1.PersistenceConfig
		if om.Spec.AppDB.PodSpec.Persistence != nil && om.Spec.AppDB.PodSpec.Persistence.SingleConfig != nil {
			config = om.Spec.AppDB.PodSpec.Persistence.SingleConfig
		}
		// Single persistence, needs to modify the only pvc we have
		pvcModification := pvcFunc(om.Spec.AppDB.DataVolumeName(), config, *defaultPodSpecPersistence.SingleConfig, om.Labels)

		// We already have, by default, the data volume mount,
		// here we also create the logs and journal one, as subpath from the same volume
		logsVolumeMount := statefulset.CreateVolumeMount(om.Spec.AppDB.DataVolumeName(), util.PvcMountPathLogs, statefulset.WithSubPath(om.Spec.AppDB.LogsVolumeName()))
		journalVolumeMount := statefulset.CreateVolumeMount(om.Spec.AppDB.DataVolumeName(), util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal))
		volumeMounts := []corev1.VolumeMount{journalVolumeMount, logsVolumeMount}
		return statefulset.Apply(
			statefulset.WithVolumeClaim(om.Spec.AppDB.DataVolumeName(), pvcModification),
			statefulset.WithPodSpecTemplate(
				podtemplatespec.Apply(
					podtemplatespec.WithContainer(construct.AgentName,
						container.WithVolumeMounts(volumeMounts),
					),
					podtemplatespec.WithContainer(construct.MongodbName,
						container.WithVolumeMounts(volumeMounts),
					),
				),
			),
		)

	} else {
		// Here need to modify data and logs volumes,
		// and create the journal one (which doesn't exist in Community, where this original STS is built)
		dataModification := pvcFunc(om.Spec.AppDB.DataVolumeName(), om.Spec.AppDB.PodSpec.Persistence.MultipleConfig.Data, *defaultPodSpecPersistence.MultipleConfig.Data, om.Labels)
		logsModification := pvcFunc(om.Spec.AppDB.LogsVolumeName(), om.Spec.AppDB.PodSpec.Persistence.MultipleConfig.Logs, *defaultPodSpecPersistence.MultipleConfig.Logs, om.Labels)

		journalVolumeMounts := statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal)
		journalVolumeClaim := pvcFunc(util.PvcNameJournal, om.Spec.AppDB.PodSpec.Persistence.MultipleConfig.Journal, *defaultPodSpecPersistence.MultipleConfig.Journal, om.Labels)

		return statefulset.Apply(
			statefulset.WithVolumeClaim(util.PvcMountPathLogs, journalVolumeClaim),
			statefulset.WithVolumeClaim(om.Spec.AppDB.DataVolumeName(), dataModification),
			statefulset.WithVolumeClaim(om.Spec.AppDB.LogsVolumeName(), logsModification),
			statefulset.WithPodSpecTemplate(
				podtemplatespec.Apply(
					podtemplatespec.WithContainer(construct.AgentName,
						container.WithVolumeMounts([]corev1.VolumeMount{journalVolumeMounts}),
					),
					podtemplatespec.WithContainer(construct.MongodbName,
						container.WithVolumeMounts([]corev1.VolumeMount{journalVolumeMounts}),
					),
				),
			),
		)
	}
}

// ShouldEnableMonitoring returns true if we need to add monitoring container (along with volume mounts) in the current reconcile loop.
func ShouldEnableMonitoring(podVars *env.PodEnvVars) bool {
	return GlobalMonitoringSettingEnabled() && podVars != nil && podVars.ProjectID != ""
}

// GlobalMonitoringSettingEnabled returns global setting whether to enable or disable monitoring in appdb (OPS_MANAGER_MONITOR_APPDB env var)
func GlobalMonitoringSettingEnabled() bool {
	return env.ReadBoolOrDefault(util.OpsManagerMonitorAppDB, util.OpsManagerMonitorAppDBDefault)
}

// ShouldMountSSLMMSCAConfigMap returns true if we need to mount MMSCA to monitoring container in the current reconcile loop.
func ShouldMountSSLMMSCAConfigMap(podVars *env.PodEnvVars) bool {
	return ShouldEnableMonitoring(podVars) && podVars.SSLMMSCAConfigMap != ""
}

// AppDbStatefulSet fully constructs the AppDb StatefulSet that is ready to be sent to the Kubernetes API server.
func AppDbStatefulSet(opsManager om.MongoDBOpsManager, podVars *env.PodEnvVars, opts AppDBStatefulSetOptions, scaler interfaces.MultiClusterReplicaSetScaler, updateStrategyType appsv1.StatefulSetUpdateStrategyType, log *zap.SugaredLogger) (appsv1.StatefulSet, error) {
	appDb := &opsManager.Spec.AppDB

	// If we can enable monitoring, let's fill in container modification function
	monitoringModification := podtemplatespec.NOOP()
	var podSpec *corev1.PodTemplateSpec
	if appDb.PodSpec != nil && appDb.PodSpec.PodTemplateWrapper.PodTemplate != nil {
		podSpec = appDb.PodSpec.PodTemplateWrapper.PodTemplate.DeepCopy()
	}

	if ShouldEnableMonitoring(podVars) {
		monitoringModification = addMonitoringContainer(*appDb, *podVars, opts, log)
	} else {
		// Otherwise, let's remove for now every podTemplateSpec related to monitoring
		// We will apply them when enabling monitoring
		if podSpec != nil {
			podSpec.Spec.Containers = removeContainerByName(podSpec.Spec.Containers, monitoringAgentContainerName)
		}
	}

	// We copy the Automation Agent command from community and add the agent startup parameters
	automationAgentCommand := construct.AutomationAgentCommand(true, opsManager.Spec.AppDB.GetAgentLogLevel(), opsManager.Spec.AppDB.GetAgentLogFile(), opsManager.Spec.AppDB.GetAgentMaxLogFileDurationHours())
	idx := len(automationAgentCommand) - 1
	automationAgentCommand[idx] += appDb.AutomationAgent.StartupParameters.ToCommandLineArgs()
	if opsManager.Spec.AppDB.IsMultiCluster() {
		automationAgentCommand[idx] += fmt.Sprintf(" -overrideLocalHost=$(hostname)-svc.${POD_NAMESPACE}.svc.%s", appDb.GetClusterDomain())
	}

	acVersionConfigMapVolume := statefulset.CreateVolumeFromConfigMap("automation-config-goal-version", opsManager.Spec.AppDB.AutomationConfigConfigMapName())
	acVersionMount := corev1.VolumeMount{
		Name:      acVersionConfigMapVolume.Name,
		ReadOnly:  true,
		MountPath: "/var/lib/automation/config/acVersion",
	}

	mod := construct.BuildMongoDBReplicaSetStatefulSetModificationFunction(&opsManager.Spec.AppDB, scaler, opts.AgentImage, true)
	if architectures.IsRunningStaticArchitecture(opsManager.Annotations) {
		mod = construct.BuildMongoDBReplicaSetStatefulSetModificationFunction(&opsManager.Spec.AppDB, scaler, opts.AgentImage, false)
	}

	sts := statefulset.New(
		mod,
		// create appdb statefulset from the community code
		statefulset.WithName(opsManager.Spec.AppDB.NameForCluster(scaler.MemberClusterNum())),
		statefulset.WithServiceName(opsManager.Spec.AppDB.HeadlessServiceNameForCluster(scaler.MemberClusterNum())),

		// If run in certified openshift bundle in disconnected environment with digest pinning we need to update
		// mongod image as it is constructed from 2 env variables and version from spec, and it will not be replaced to sha256 digest properly.
		// The official image provides both CMD and ENTRYPOINT. We're reusing the former and need to replace
		// the latter with an empty string.
		containerImageModification(construct.MongodbName, opts.MongodbImage, []string{""}),
		// we don't need to update here the automation agent image for digest pinning, because it is defined in AGENT_IMAGE env var as full url with version
		// if we run in certified bundle with digest pinning it will be properly updated to digest
		customPersistenceConfig(&opsManager),
		statefulset.WithUpdateStrategyType(updateStrategyType),
		statefulset.WithOwnerReference(kube.BaseOwnerReference(&opsManager)),
		statefulset.WithReplicas(scale.ReplicasThisReconciliation(scaler)),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podtemplatespec.WithServiceAccount(appDBServiceAccount),
				podtemplatespec.WithVolume(acVersionConfigMapVolume),
				podtemplatespec.WithContainer(construct.AgentName,
					container.Apply(
						container.WithCommand(automationAgentCommand),
						container.WithEnvs(appdbContainerEnv(*appDb)...),
						container.WithVolumeMounts([]corev1.VolumeMount{acVersionMount}),
					),
				),
				vaultModification(*appDb, podVars, opts),
				appDbPodSpec(opts.InitAppDBImage, opsManager),
				monitoringModification,
				tlsVolumes(*appDb, podVars, log),
			),
		),
		appDbLabels(&opsManager, scaler.MemberClusterNum()),
	)

	// We merge the podspec specified in the CR
	if podSpec != nil {
		sts.Spec = merge.StatefulSetSpecs(sts.Spec, appsv1.StatefulSetSpec{Template: *podSpec})
	}
	return sts, nil
}

// IsEnterprise returns whether the set image in activated with the enterprise module.
// By default, it should be true, but
// for safety mechanisms we implement a backdoor to deactivate the enterprise AC feature.
func IsEnterprise() bool {
	overrideAssumption, err := strconv.ParseBool(os.Getenv(construct.MongoDBAssumeEnterpriseEnv))
	if err == nil {
		return overrideAssumption
	}
	return true
}

func GetOfficialImage(imageUrls ImageUrls, version string, annotations map[string]string) string {
	repoUrl := imageUrls[construct.MongodbRepoUrl]
	// TODO: rethink the logic of handling custom image types. We are currently only handling ubi9 and ubi8 and we never
	// were really handling erroneus types, we just leave them be if specified (e.g. -ubuntu).
	// envvar.GetEnvOrDefault(construct.MongoDBImageType, string(architectures.DefaultImageType))
	var imageType string

	if architectures.IsRunningStaticArchitecture(annotations) {
		imageType = string(architectures.ImageTypeUBI9)
	} else {
		// For non-static architecture, we need to default to UBI8 to support customers running MongoDB versions < 6.0.4,
		// which don't have UBI9 binaries.
		imageType = string(architectures.ImageTypeUBI8)
	}

	imageURL := imageUrls[construct.MongodbImageEnv]

	if strings.HasSuffix(repoUrl, "/") {
		repoUrl = strings.TrimRight(repoUrl, "/")
	}

	assumeOldFormat := envvar.ReadBool(util.MdbAppdbAssumeOldFormat)
	if IsEnterpriseImage(imageURL) && !assumeOldFormat {
		// 5.0.6-ent -> 5.0.6-ubi8
		if strings.HasSuffix(version, "-ent") {
			version = fmt.Sprintf("%s%s", strings.TrimSuffix(version, "ent"), imageType)
		}
		// 5.0.6 ->  5.0.6-ubi8
		r := regexp.MustCompile("-.+$")
		if !r.MatchString(version) {
			version = version + "-" + imageType
		}
		if found, suffix := architectures.HasSupportedImageTypeSuffix(version); found {
			version = fmt.Sprintf("%s%s", strings.TrimSuffix(version, suffix), imageType)
		}
		// if neither, let's not change it: 5.0.6-ubi8 -> 5.0.6-ubi8
	}

	mongoImageName := ContainerImage(imageUrls, construct.MongodbImageEnv, version)

	if strings.Contains(mongoImageName, "@sha256:") || strings.HasPrefix(mongoImageName, repoUrl) {
		return mongoImageName
	}

	return fmt.Sprintf("%s/%s", repoUrl, mongoImageName)
}

func containerImageModification(containerName string, image string, args []string) statefulset.Modification {
	return func(sts *appsv1.StatefulSet) {
		for i, c := range sts.Spec.Template.Spec.Containers {
			if c.Name == containerName {
				c.Image = image
				c.Args = args
				sts.Spec.Template.Spec.Containers[i] = c
				break
			}
		}
	}
}

// getVolumeMountIndexByName returns the volume mount with the given name from the inut slice.
// It returns -1 if this doesn't exist
func getVolumeMountIndexByName(mounts []corev1.VolumeMount, name string) int {
	for i, mount := range mounts {
		if mount.Name == name {
			return i
		}
	}
	return -1
}

// addMonitoringContainer returns a podtemplatespec modification that adds the monitoring container to the AppDB Statefulset.
// Note that this replicates some code from the functions that do this for the base AppDB Statefulset. After many iterations
// this was deemed to be an acceptable compromise to make code clearer and more maintainable.
func addMonitoringContainer(appDB om.AppDBSpec, podVars env.PodEnvVars, opts AppDBStatefulSetOptions, log *zap.SugaredLogger) podtemplatespec.Modification {
	var monitoringAcVolume corev1.Volume
	var monitoringACFunc podtemplatespec.Modification

	monitoringConfigMapVolume := statefulset.CreateVolumeFromConfigMap("monitoring-automation-config-goal-version", appDB.MonitoringAutomationConfigConfigMapName())
	monitoringConfigMapVolumeFunc := podtemplatespec.WithVolume(monitoringConfigMapVolume)

	if vault.IsVaultSecretBackend() {
		secretsToInject := vault.AppDBSecretsToInject{Config: opts.VaultConfig}
		secretsToInject.AutomationConfigSecretName = appDB.MonitoringAutomationConfigSecretName()
		secretsToInject.AutomationConfigPath = util.AppDBMonitoringAutomationConfigKey
		secretsToInject.AgentType = "monitoring-agent"
		monitoringACFunc = podtemplatespec.WithAnnotations(secretsToInject.AppDBAnnotations(appDB.Namespace))
	} else {
		// Create a volume to store the monitoring automation config.
		// This is different from the AC for the automation agent, since:
		// - It contains entries for "MonitoringVersions"
		// - It has empty entries for ReplicaSets and Processes
		monitoringAcVolume = statefulset.CreateVolumeFromSecret("monitoring-automation-config", appDB.MonitoringAutomationConfigSecretName())
		monitoringACFunc = podtemplatespec.WithVolume(monitoringAcVolume)
	}
	// Construct the command by concatenating:
	// 1. The base command - from community
	command := construct.MongodbUserCommandWithAPIKeyExport
	command += "agent/mongodb-agent"
	command += " -healthCheckFilePath=" + monitoringAgentHealthStatusFilePathValue
	command += " -serveStatusPort=5001"
	command += getMonitoringAgentLogOptions(appDB)

	// 2. Add the cluster config file path
	// If we are using k8s secrets, this is the same as community (and the same as the other agent container)
	// But this is not possible in vault so we need two separate paths
	if vault.IsVaultSecretBackend() {
		command += " -cluster=/var/lib/automation/config/" + util.AppDBMonitoringAutomationConfigKey
	} else {
		command += " -cluster=/var/lib/automation/config/" + util.AppDBAutomationConfigKey
	}

	// 2. Startup parameters for the agent to enable monitoring
	startupParams := mdbv1.StartupParameters{
		"mmsApiKey":  "${AGENT_API_KEY}",
		"mmsGroupId": podVars.ProjectID,
	}

	// 3. Startup parameters for the agent to enable TLS
	if podVars.SSLMMSCAConfigMap != "" {
		trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
		startupParams["sslTrustedMMSServerCertificate"] = trustedCACertLocation
	}

	if podVars.SSLRequireValidMMSServerCertificates {
		startupParams["sslRequireValidMMSServerCertificates"] = "true"
	}

	// 4. Custom startup parameters provided in the CR
	// By default appDB.AutomationAgent.StartupParameters apply to both agents
	// if appDB.MonitoringAgent.StartupParameters is specified, it overrides the former
	monitoringStartupParams := appDB.AutomationAgent.StartupParameters
	if appDB.MonitoringAgent.StartupParameters != nil {
		monitoringStartupParams = appDB.MonitoringAgent.StartupParameters
	}

	for k, v := range monitoringStartupParams {
		startupParams[k] = v
	}

	command += startupParams.ToCommandLineArgs()

	if appDB.IsMultiCluster() {
		command += fmt.Sprintf(" -overrideLocalHost=$(hostname)-svc.${POD_NAMESPACE}.svc.%s", appDB.GetClusterDomain())
	}

	monitoringCommand := []string{"/bin/bash", "-c", command}

	// Add additional TLS volumes if needed
	_, monitoringMounts := getTLSVolumesAndVolumeMounts(appDB, &podVars, log)

	return podtemplatespec.Apply(
		monitoringACFunc,
		monitoringConfigMapVolumeFunc,
		// This is a function that reads the automation agent containers, copies it and modifies it.
		// We do this since the two containers are very similar with just a few differences
		func(podTemplateSpec *corev1.PodTemplateSpec) {
			monitoringContainer := podtemplatespec.FindContainerByName(construct.AgentName, podTemplateSpec).DeepCopy()
			monitoringContainer.Name = monitoringAgentContainerName

			// we ensure that the monitoring agent image is compatible with the input of Ops Manager we're using.
			// once we make static containers the default, we can remove this code.
			if opts.LegacyMonitoringAgentImage != "" {
				monitoringContainer.Image = opts.LegacyMonitoringAgentImage
			}

			// Replace the automation config volume
			volumeMounts := monitoringContainer.VolumeMounts
			if !vault.IsVaultSecretBackend() {
				// Replace the automation config volume
				acMountIndex := getVolumeMountIndexByName(volumeMounts, "automation-config")
				if acMountIndex != -1 {
					volumeMounts[acMountIndex].Name = monitoringAcVolume.Name
				}
			}

			configMapIndex := getVolumeMountIndexByName(volumeMounts, "automation-config-goal-input")
			if configMapIndex != -1 {
				volumeMounts[configMapIndex].Name = monitoringConfigMapVolume.Name
			}

			tmpVolumeMountIndex := getVolumeMountIndexByName(volumeMounts, util.PvcNameTmp)
			if tmpVolumeMountIndex != -1 {
				volumeMounts[tmpVolumeMountIndex].SubPath = tmpSubpathName
			}

			// Set up custom persistence options - see customPersistenceConfig() for an explanation
			if appDB.HasSeparateDataAndLogsVolumes() {
				journalVolumeMounts := statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal)
				volumeMounts = append(volumeMounts, journalVolumeMounts)
			} else {
				logsVolumeMount := statefulset.CreateVolumeMount(appDB.DataVolumeName(), util.PvcMountPathLogs, statefulset.WithSubPath(appDB.LogsVolumeName()))
				journalVolumeMount := statefulset.CreateVolumeMount(appDB.DataVolumeName(), util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal))
				volumeMounts = append(volumeMounts, journalVolumeMount, logsVolumeMount)
			}

			if !vault.IsVaultSecretBackend() {
				// AGENT_API_KEY volume
				volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(AgentAPIKeyVolumeName, AgentAPIKeySecretPath))
			}
			container.Apply(
				container.WithVolumeMounts(volumeMounts),
				container.WithCommand(monitoringCommand),
				container.WithResourceRequirements(buildRequirementsFromPodSpec(*NewDefaultPodSpecWrapper(*appDB.PodSpec))),
				container.WithVolumeMounts(monitoringMounts),
				container.WithEnvs(appdbContainerEnv(appDB)...),
				container.WithEnvs(readinessEnvironmentVariablesToEnvVars(appDB.AutomationAgent.ReadinessProbe.EnvironmentVariables)...),
			)(monitoringContainer)
			podTemplateSpec.Spec.Containers = append(podTemplateSpec.Spec.Containers, *monitoringContainer)
		},
	)
}

// appdbContainerEnv returns the set of env var needed by the AppDB.
func appdbContainerEnv(appDbSpec om.AppDBSpec) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      podNamespaceEnv,
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			Name:  automationConfigMapEnv,
			Value: appDbSpec.Name() + "-config",
		},
		{
			Name:  headlessAgentEnv,
			Value: "true",
		},
		{
			Name:  clusterDomainEnv,
			Value: appDbSpec.ClusterDomain,
		},
	}
	return envVars
}
