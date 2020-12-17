package construct

import (
	"fmt"
	"path"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/scale"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// agentLibPath defines the base path for agent configuration files including the automation
	// config file for the headless agent,
	agentLibPath = "/var/lib/mongodb-automation/"
	// clusterConfigVolumeName is the name of the volume resource.
	clusterConfigVolumeName    = "cluster-config"
	appDBServiceAccount        = "mongodb-enterprise-appdb"
	initAppDbContainerName     = "mongodb-enterprise-init-appdb"
	appDbReadinessProbeCommand = "/opt/scripts/readinessprobe"
	appDbLivenessProbeCommand  = "/opt/scripts/probe.sh"
	// AppDB environment variable names
	appDBAutomationAgentVersionEnv = "APPDB_AUTOMATION_AGENT_VERSION"
	initAppdbVersionEnv            = "INIT_APPDB_VERSION"
	podNamespaceEnv                = "POD_NAMESPACE"
	automationConfigMapEnv         = "AUTOMATION_CONFIG_MAP"
	headlessAgentEnv               = "HEADLESS_AGENT"
	agentApiKeyEnv                 = "AGENT_API_KEY"
)

// AppDbStatefulSet fully constructs the AppDb StatefulSet that is ready to be sent to the Kubernetes API server.
// A list of optional configuration options can be provided to make any modifications that are required.
// TODO: this will be AppDbStatefulSet in the next PR
func AppDbStatefulSetNew(opsManager om.MongoDBOpsManager, opts ...func(options *DatabaseStatefulSetOptions)) (appsv1.StatefulSet, error) {
	appDb := opsManager.Spec.AppDB

	// Providing the default size of pod as otherwise sometimes the agents in pod complain about not enough memory
	// on mongodb download: "write /tmp/mms-automation/test/versions/mongodb-linux-x86_64-4.0.0/bin/mongo: cannot
	// allocate memory"
	appdbPodSpec := newDefaultPodSpecWrapper(*appDb.PodSpec)
	appdbPodSpec.Default.MemoryRequests = util.DefaultMemoryAppDB

	_ = DatabaseStatefulSetOptions{
		Replicas:       scale.ReplicasThisReconciliation(&opsManager),
		Name:           appDb.Name(),
		ServiceName:    appDb.ServiceName(),
		PodSpec:        appdbPodSpec,
		ServicePort:    appDb.AdditionalMongodConfig.GetPortOrDefault(),
		Persistent:     appDb.Persistent,
		OwnerReference: kube.BaseOwnerReference(&opsManager),
		AgentConfig:    appDb.MongoDbSpec.Agent,
	}

	// TODO: future PR
	return appsv1.StatefulSet{}, nil
}

// AppDbStatefulSet fully constructs the AppDB StatefulSet
func AppDbStatefulSet(mdbBuilder DatabaseBuilder) appsv1.StatefulSet {
	templateFunc := buildAppDBPodTemplateSpecFunc(mdbBuilder)
	return statefulset.New(buildDatabaseStatefulSetConfigurationFunction(mdbBuilder, templateFunc))
}

// buildAppDBPodTemplateSpecFunc constructs the appDb podTemplateSpec modification function
func buildAppDBPodTemplateSpecFunc(mdbBuilder DatabaseBuilder) podtemplatespec.Modification {
	// AppDB only uses the automation agent in headless mode, let's use the latest version
	appdbImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.AppDBImageUrl),
		env.ReadOrDefault(appDBAutomationAgentVersionEnv, "latest"))

	var volumeMounts []corev1.VolumeMount
	var volumes []corev1.Volume

	automationConfigVolume := statefulset.CreateVolumeFromSecret(clusterConfigVolumeName, mdbBuilder.GetName()+"-config")
	// automationConfigVolume is only required by the AppDB database container
	volumes = append(volumes, automationConfigVolume)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      automationConfigVolume.Name,
		MountPath: agentLibPath,
		ReadOnly:  true,
	})

	// scripts volume is shared by the init container and the AppDB so the startup
	// script can be copied over
	scriptsVolume := statefulset.CreateVolumeFromEmptyDir("appdb-scripts")
	volumes = append(volumes, scriptsVolume)
	volumeMounts = append(volumeMounts, appDbScriptsVolumeMount(true))

	if mdbBuilder.GetSSLMMSCAConfigMap() != "" {
		caCertVolume := statefulset.CreateVolumeFromConfigMap(caCertName, mdbBuilder.GetSSLMMSCAConfigMap())
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: caCertMountPath,
			Name:      caCertVolume.Name,
			ReadOnly:  true,
		})
		volumes = append(volumes, caCertVolume)
	}

	addVolumes := func(spec *corev1.PodTemplateSpec) {
		for _, v := range volumes {
			podtemplatespec.WithVolume(v)(spec)
		}
	}

	return podtemplatespec.Apply(
		sharedDatabaseConfiguration(mdbBuilder),
		podtemplatespec.WithAnnotations(map[string]string{}),
		podtemplatespec.WithServiceAccount(appDBServiceAccount),
		addVolumes,
		podtemplatespec.WithInitContainerByIndex(0,
			buildAppdbInitContainer(),
		),
		podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithName(util.AppDbContainerName),
				container.WithImage(appdbImageURL),
				container.WithEnvs(appdbContainerEnv(mdbBuilder)...),
				container.WithReadinessProbe(buildAppDbReadinessProbe()),
				container.WithLivenessProbe(buildAppDbLivenessProbe()),
				container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
				withVolumeMounts(volumeMounts),
			),
		),
	)
}

// appDbScriptsVolumeMount constructs the VolumeMount for the appDB scripts
// this should be readonly for the AppDB, and not read only for the init container.
func appDbScriptsVolumeMount(readOnly bool) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      "appdb-scripts",
		MountPath: "/opt/scripts",
		ReadOnly:  readOnly,
	}
}

func buildAppdbInitContainer() container.Modification {
	version := env.ReadOrDefault(initAppdbVersionEnv, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.InitAppdbImageUrl), version)

	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(defaultSecurityContext())
	}

	return container.Apply(
		container.WithName(initAppDbContainerName),
		container.WithImage(initContainerImageURL),
		configureContainerSecurityContext,
		withVolumeMounts([]corev1.VolumeMount{
			appDbScriptsVolumeMount(false),
		}),
	)
}

func appdbContainerEnv(mdbBuilder DatabaseBuilder) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      podNamespaceEnv,
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			Name: automationConfigMapEnv,
			// not critical but would be nice to reuse `AppDB.AutomationConfigSecretName`
			Value: mdbBuilder.GetName() + "-config",
		},
		{
			Name:  headlessAgentEnv,
			Value: "true",
		},
	}

	// These env vars are required to configure Monitoring of the AppDB
	if mdbBuilder.GetProjectID() != "" {
		envVars = append(envVars, envVarFromSecret(agentApiKeyEnv, agentApiKeySecretName(mdbBuilder.GetProjectID()), util.OmAgentApiKey))
		envVars = append(envVars, corev1.EnvVar{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: mdbBuilder.GetProjectID(),
		})
		envVars = append(envVars, corev1.EnvVar{
			Name:  util.ENV_VAR_USER,
			Value: mdbBuilder.GetUser(),
		})

		if mdbBuilder.GetSSLMMSCAConfigMap() != "" {
			// A custom CA has been provided, point the trusted CA to the location of custom CAs
			trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
			envVars = append(envVars,
				corev1.EnvVar{
					Name:  util.EnvVarSSLTrustedMMSServerCertificate,
					Value: trustedCACertLocation,
				},
			)
		}

	}

	return envVars
}

func buildAppDbLivenessProbe() probes.Modification {
	return probes.Apply(
		databaseReadinessProbe(),
		probes.WithExecCommand([]string{appDbLivenessProbeCommand}),
	)

}

func buildAppDbReadinessProbe() probes.Modification {
	return probes.Apply(
		probes.WithPeriodSeconds(5),
		probes.WithExecCommand([]string{appDbReadinessProbeCommand}),
		probes.WithInitialDelaySeconds(5),
		probes.WithFailureThreshold(60),
	)
}
