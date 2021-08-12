package construct

import (
	"fmt"
	"path"
	"strings"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/om"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	construct "github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	appDBServiceAccount    = "mongodb-enterprise-appdb"
	InitAppDbContainerName = "mongodb-enterprise-init-appdb"
	// AppDB environment variable names
	initAppdbVersionEnv    = "INIT_APPDB_VERSION"
	podNamespaceEnv        = "POD_NAMESPACE"
	automationConfigMapEnv = "AUTOMATION_CONFIG_MAP"
	headlessAgentEnv       = "HEADLESS_AGENT"
	agentApiKeyEnv         = "AGENT_API_KEY"

	monitoringAgentContainerName = "mongodb-agent-monitoring"
)

func getContainerIndexByName(containers []corev1.Container, name string) int {
	for i, container := range containers {
		if container.Name == name {
			return i
		}
	}
	return -1
}

func appDbLabels(opsManager om.MongoDBOpsManager) statefulset.Modification {
	podLabels := map[string]string{
		appLabelKey:             opsManager.Spec.AppDB.ServiceName(),
		ControllerLabelName:     util.OperatorName,
		PodAntiAffinityLabelKey: opsManager.Spec.AppDB.Name(),
	}
	return statefulset.Apply(
		statefulset.WithMatchLabels(podLabels),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podtemplatespec.WithPodLabels(podLabels),
			),
		),
	)
}

func appDbPodSpec(appDb om.AppDBSpec) podtemplatespec.Modification {
	appdbPodSpec := newDefaultPodSpecWrapper(*appDb.PodSpec)
	mongoPodSpec := appdbPodSpec
	mongoPodSpec.Default.MemoryRequests = util.DefaultMemoryAppDB
	mongoPodTemplateFunc := podtemplatespec.WithContainer(
		construct.MongodbName,
		container.WithResourceRequirements(buildRequirementsFromPodSpec(*mongoPodSpec)),
	)
	automationPodTemplateFunc := podtemplatespec.WithContainer(
		construct.AgentName,
		container.WithResourceRequirements(buildRequirementsFromPodSpec(*appdbPodSpec)),
	)

	// the appdb will have a single init container, all of the necessary binaries will be copied into the various
	// volumes of different containers.
	updateInitContainers := func(templateSpec *corev1.PodTemplateSpec) {
		templateSpec.Spec.InitContainers = []corev1.Container{}
		scriptsVolumeMount := statefulset.CreateVolumeMount("agent-scripts", "/opt/scripts", statefulset.WithReadOnly(false))
		hooksVolumeMount := statefulset.CreateVolumeMount("hooks", "/hooks", statefulset.WithReadOnly(false))
		podtemplatespec.WithInitContainer(InitAppDbContainerName, buildAppDBInitContainer([]corev1.VolumeMount{scriptsVolumeMount, hooksVolumeMount}))(templateSpec)
	}

	return podtemplatespec.Apply(
		mongoPodTemplateFunc,
		automationPodTemplateFunc,
		updateInitContainers,
	)
}

// buildAppDBInitContainer builds the container specification for mongodb-enterprise-init-appdb image
func buildAppDBInitContainer(volumeMounts []corev1.VolumeMount) container.Modification {
	version := env.ReadOrDefault(initAppdbVersionEnv, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.InitAppdbImageUrlEnv), version)

	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(defaultSecurityContext())
	}
	return container.Apply(
		container.WithName(InitAppDbContainerName),
		container.WithImage(initContainerImageURL),
		container.WithCommand([]string{"/bin/sh", "-c", `

# the agent requires the readiness probe
cp /probes/readinessprobe /opt/scripts/readinessprobe

# the mongod requires the version upgrade hook
cp /probes/version-upgrade-hook /hooks/version-upgrade
`}),
		configureContainerSecurityContext,
		container.WithVolumeMounts(volumeMounts),
	)
}

func getTLSVolumesAndVolumeMounts(appDb om.AppDBSpec, podVars *env.PodEnvVars) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumesToAdd []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	if appDb.Security != nil {
		tlsConfig := appDb.Security.TLSConfig
		if tlsConfig.IsEnabled() {
			// In this location the certificates will be linked -s into server.pem
			secretName := fmt.Sprintf("%s-cert", appDb.Name())

			if tlsConfig.SecretRef.Prefix != "" {
				// Certificates will be used from the secret with the corresponding prefix.
				secretName = fmt.Sprintf("%s-%s-cert", tlsConfig.SecretRef.Prefix, appDb.Name())
			}

			if tlsConfig.SecretRef.Name != "" {
				secretName = tlsConfig.SecretRef.Name
			}
			secretVolume := statefulset.CreateVolumeFromSecret(util.SecretVolumeName, secretName)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: util.SecretVolumeMountPath + "/certs",
				Name:      secretVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, secretVolume)
		}

		if tlsConfig.CA != "" {
			caVolume := statefulset.CreateVolumeFromConfigMap(ConfigMapVolumeCAName, tlsConfig.CA)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: util.ConfigMapVolumeCAMountPath,
				Name:      caVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, caVolume)
		}
	}

	if podVars != nil && podVars.SSLMMSCAConfigMap != "" {
		caCertVolume := statefulset.CreateVolumeFromConfigMap(CaCertName, podVars.SSLMMSCAConfigMap)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: caCertMountPath,
			Name:      caCertVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, caCertVolume)
	}
	return volumesToAdd, volumeMounts
}

func tlsVolumes(appDb om.AppDBSpec, podVars *env.PodEnvVars) podtemplatespec.Modification {

	volumesToAdd, volumeMounts := getTLSVolumesAndVolumeMounts(appDb, podVars)
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

func customPersistenceConfig(appDb om.AppDBSpec) statefulset.Modification {
	defaultPodSpecPersistence := newDefaultPodSpec().Persistence
	if !appDb.HasSeparateDataAndLogsVolumes() {
		var config *mdbv1.PersistenceConfig
		if appDb.PodSpec.Persistence != nil && appDb.PodSpec.Persistence.SingleConfig != nil {
			config = appDb.PodSpec.Persistence.SingleConfig
		}
		// Single persistence, needs to modify the only pvc we have
		pvcModification := pvcFunc(appDb.DataVolumeName(), config, *defaultPodSpecPersistence.SingleConfig)

		logsVolumeMount := statefulset.CreateVolumeMount(appDb.DataVolumeName(), util.PvcMountPathLogs, statefulset.WithSubPath(appDb.LogsVolumeName()))
		journalVolumeMount := statefulset.CreateVolumeMount(appDb.DataVolumeName(), util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal))
		volumeMounts := []corev1.VolumeMount{journalVolumeMount, logsVolumeMount}
		return statefulset.Apply(
			statefulset.WithVolumeClaim(appDb.DataVolumeName(), pvcModification),
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
		// need to modify data, logs, and create journal
		dataModification := pvcFunc(appDb.DataVolumeName(), appDb.PodSpec.Persistence.MultipleConfig.Data, *defaultPodSpecPersistence.MultipleConfig.Data)
		logsModification := pvcFunc(appDb.LogsVolumeName(), appDb.PodSpec.Persistence.MultipleConfig.Logs, *defaultPodSpecPersistence.MultipleConfig.Logs)

		journalVolumeMounts := statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal)
		journalVolumeClaim := pvcFunc(util.PvcNameJournal, appDb.PodSpec.Persistence.MultipleConfig.Journal, *defaultPodSpecPersistence.MultipleConfig.Journal)

		return statefulset.Apply(
			statefulset.WithVolumeClaim(util.PvcMountPathLogs, journalVolumeClaim),
			statefulset.WithVolumeClaim(appDb.DataVolumeName(), dataModification),
			statefulset.WithVolumeClaim(appDb.LogsVolumeName(), logsModification),
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

func removeContainerByName(containers []corev1.Container, name string) []corev1.Container {
	index := getContainerIndexByName(containers, name)
	if index == -1 {
		return containers
	}
	return append(containers[:index], containers[index+1:]...)
}

// AppDbStatefulSet fully constructs the AppDb StatefulSet that is ready to be sent to the Kubernetes API server.
func AppDbStatefulSet(opsManager om.MongoDBOpsManager, podVars *env.PodEnvVars, monitoringAgentVersion string) (appsv1.StatefulSet, error) {
	appDb := &opsManager.Spec.AppDB

	// If we can enable monitoring, let's fill in container modification function
	monitoringModification := podtemplatespec.NOOP()
	monitorAppDB := env.ReadBoolOrDefault(util.OpsManagerMonitorAppDB, util.OpsManagerMonitorAppDBDefault)
	if monitorAppDB && podVars != nil && podVars.ProjectID != "" {
		monitoringModification = addMonitoringContainer(*appDb, *podVars, monitoringAgentVersion)
	} else {
		// Otherwise, let's remove for now every podTemplateSpec related to monitoring
		if appDb.PodSpec != nil && appDb.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			appDb.PodSpec.PodTemplateWrapper.PodTemplate.Spec.Containers = removeContainerByName(appDb.PodSpec.PodTemplateWrapper.PodTemplate.Spec.Containers, monitoringAgentContainerName)
		}
	}

	automationAgentCommand := construct.AutomationAgentCommand()
	idx := len(automationAgentCommand) - 1
	automationAgentCommand[idx] += appDb.AutomationAgent.StartupParameters.ToCommandLineArgs()

	sts := statefulset.New(
		construct.BuildMongoDBReplicaSetStatefulSetModificationFunction(opsManager.Spec.AppDB, opsManager),
		customPersistenceConfig(*appDb),
		statefulset.WithUpdateStrategyType(opsManager.GetAppDBUpdateStrategyType()),
		statefulset.WithOwnerReference(kube.BaseOwnerReference(&opsManager)),
		statefulset.WithReplicas(scale.ReplicasThisReconciliation(&opsManager)),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podtemplatespec.WithServiceAccount(appDBServiceAccount),
				podtemplatespec.WithContainer(construct.AgentName,
					container.Apply(
						container.WithCommand(automationAgentCommand),
						container.WithEnvs(appdbContainerEnv(*appDb, podVars)...),
					),
				),
				appDbPodSpec(*appDb),
				monitoringModification,
				tlsVolumes(*appDb, podVars),
			),
		),
		appDbLabels(opsManager),
	)
	// We merge the podspec sepcified in the CR
	if appDb.PodSpec != nil && appDb.PodSpec.PodTemplateWrapper.PodTemplate != nil {
		sts.Spec = merge.StatefulSetSpecs(sts.Spec, appsv1.StatefulSetSpec{Template: *appDb.PodSpec.PodTemplateWrapper.PodTemplate})
	}
	return sts, nil
}

func getVolumeMountIndexByName(mounts []corev1.VolumeMount, name string) int {
	for i, mount := range mounts {
		if mount.Name == name {
			return i
		}
	}
	return -1
}

// replaceImageTag returns the image with the tag replaced.
func replaceImageTag(image string, newTag string) string {
	imageSplit := strings.Split(image, ":")
	imageSplit[len(imageSplit)-1] = newTag
	return strings.Join(imageSplit, ":")
}

func addMonitoringContainer(appDB om.AppDBSpec, podVars env.PodEnvVars, monitoringAgentVerison string) podtemplatespec.Modification {
	monitoringAcVolume := statefulset.CreateVolumeFromSecret("monitoring-automation-config", appDB.MonitoringAutomationConfigSecretName())

	command := construct.MongodbUserCommand + construct.BaseAgentCommand()

	startupParams := mdbv1.StartupParameters{
		"mmsApiKey":  "$(AGENT_API_KEY)",
		"mmsGroupId": podVars.ProjectID,
	}

	if podVars.SSLMMSCAConfigMap != "" {
		trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
		startupParams["sslTrustedMMSServerCertificate"] = trustedCACertLocation
	}

	if podVars.SSLRequireValidMMSServerCertificates {
		startupParams["sslRequireValidMMSServerCertificates"] = ""
	}

	monitoringStartupParams := appDB.AutomationAgent.StartupParameters
	if appDB.MonitoringAgent.StartupParameters != nil {
		monitoringStartupParams = appDB.MonitoringAgent.StartupParameters
	}

	for k, v := range monitoringStartupParams {
		startupParams[k] = v
	}

	command += startupParams.ToCommandLineArgs()

	monitoringCommand := []string{"/bin/bash", "-c", command}

	_, monitoringMounts := getTLSVolumesAndVolumeMounts(appDB, &podVars)
	return podtemplatespec.Apply(
		podtemplatespec.WithVolume(monitoringAcVolume),
		func(podTemplateSpec *corev1.PodTemplateSpec) {
			monitoringContainer := podtemplatespec.FindContainerByName(construct.AgentName, podTemplateSpec).DeepCopy()
			monitoringContainer.Name = monitoringAgentContainerName

			// we ensure that the monitoring agent image is compatible with the version of Ops Manager we're using.
			if monitoringAgentVerison != "" {
				monitoringContainer.Image = replaceImageTag(monitoringContainer.Image, monitoringAgentVerison)
			}
			volumeMounts := monitoringContainer.VolumeMounts
			acMountIndex := getVolumeMountIndexByName(volumeMounts, "automation-config")
			if acMountIndex == -1 {
				return
			}
			volumeMounts[acMountIndex].Name = monitoringAcVolume.Name
			if appDB.HasSeparateDataAndLogsVolumes() {
				journalVolumeMounts := statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal)
				volumeMounts = append(volumeMounts, journalVolumeMounts)
			} else {
				logsVolumeMount := statefulset.CreateVolumeMount(appDB.DataVolumeName(), util.PvcMountPathLogs, statefulset.WithSubPath(appDB.LogsVolumeName()))
				journalVolumeMount := statefulset.CreateVolumeMount(appDB.DataVolumeName(), util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal))
				volumeMounts = append(volumeMounts, journalVolumeMount, logsVolumeMount)
			}
			container.Apply(
				container.WithVolumeMounts(volumeMounts),
				container.WithCommand(monitoringCommand),
				container.WithResourceRequirements(buildRequirementsFromPodSpec(*newDefaultPodSpecWrapper(*appDB.PodSpec))),
				container.WithVolumeMounts(monitoringMounts),
				container.WithEnvs(appdbContainerEnv(appDB, &podVars)...),
			)(monitoringContainer)
			podTemplateSpec.Spec.Containers = append(podTemplateSpec.Spec.Containers, *monitoringContainer)
		},
	)
}

func appdbContainerEnv(appDbSpec om.AppDBSpec, podVars *env.PodEnvVars) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      podNamespaceEnv,
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			// TODO CLOUDP-85548 the readiness would need the correct name for monitoring
			Name:  automationConfigMapEnv,
			Value: appDbSpec.Name() + "-config",
		},
		{
			Name:  headlessAgentEnv,
			Value: "true",
		},
	}

	// These env vars are required to configure Monitoring of the AppDB
	if podVars != nil && podVars.ProjectID != "" {
		envVars = append(envVars, env.FromSecret(agentApiKeyEnv, agentApiKeySecretName(podVars.ProjectID), util.OmAgentApiKey))
	}

	return envVars
}
