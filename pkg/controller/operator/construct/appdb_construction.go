package construct

import (
	"fmt"
	"path"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/scale"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	appDBAutomationAgentVersionEnv = "APPDB_AGENT_VERSION"
	initAppdbVersionEnv            = "INIT_APPDB_VERSION"
	podNamespaceEnv                = "POD_NAMESPACE"
	automationConfigMapEnv         = "AUTOMATION_CONFIG_MAP"
	headlessAgentEnv               = "HEADLESS_AGENT"
	agentApiKeyEnv                 = "AGENT_API_KEY"
)

func AppDbOptions(opts ...func(options *DatabaseStatefulSetOptions)) func(opsManager om.MongoDBOpsManager) DatabaseStatefulSetOptions {
	return func(opsManager om.MongoDBOpsManager) DatabaseStatefulSetOptions {
		appDb := opsManager.Spec.AppDB

		// Providing the default size of pod as otherwise sometimes the agents in pod complain about not enough memory
		// on mongodb download: "write /tmp/mms-automation/test/versions/mongodb-linux-x86_64-4.0.0/bin/mongo: cannot
		// allocate memory"
		appdbPodSpec := newDefaultPodSpecWrapper(*appDb.PodSpec)
		appdbPodSpec.Default.MemoryRequests = util.DefaultMemoryAppDB

		stsOpts := DatabaseStatefulSetOptions{
			Replicas:       scale.ReplicasThisReconciliation(&opsManager),
			Name:           appDb.Name(),
			ServiceName:    appDb.ServiceName(),
			PodSpec:        appdbPodSpec,
			ServicePort:    appDb.AdditionalMongodConfig.GetPortOrDefault(),
			Persistent:     appDb.Persistent,
			OwnerReference: kube.BaseOwnerReference(&opsManager),
			AgentConfig:    appDb.MongoDbSpec.Agent,
		}

		for _, opt := range opts {
			opt(&stsOpts)
		}
		return stsOpts
	}
}

// AppDbStatefulSet fully constructs the AppDb StatefulSet that is ready to be sent to the Kubernetes API server.
// A list of optional configuration options can be provided to make any modifications that are required.
func AppDbStatefulSet(opsManager om.MongoDBOpsManager, opts ...func(options *DatabaseStatefulSetOptions)) (appsv1.StatefulSet, error) {
	stsOpts := AppDbOptions(opts...)(opsManager)
	// TODO: temporary way of using the same function to build both appdb and databaes
	// this will be cleaned up in a future PR
	mdb := mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opsManager.Spec.AppDB.Name(),
			Namespace: opsManager.Namespace,
		},
		Spec: opsManager.Spec.AppDB.MongoDbSpec,
	}

	dbSts := appDbDatabaseStatefulSet(mdb, &stsOpts)
	if mdb.Spec.PodSpec != nil && mdb.Spec.PodSpec.PodTemplate != nil {
		return enterprisests.MergeSpec(dbSts, &appsv1.StatefulSetSpec{Template: *mdb.Spec.PodSpec.PodTemplate})
	}
	return dbSts, nil
}

func appDbDatabaseStatefulSet(mdb mdbv1.MongoDB, stsOpts *DatabaseStatefulSetOptions) appsv1.StatefulSet {
	templateFunc := buildAppDBPodTemplateSpecFunc(*stsOpts)
	return statefulset.New(buildDatabaseStatefulSetConfigurationFunction(mdb, templateFunc, *stsOpts))
}

// buildAppDBPodTemplateSpecFunc constructs the appDb podTemplateSpec modification function
func buildAppDBPodTemplateSpecFunc(opts DatabaseStatefulSetOptions) podtemplatespec.Modification {
	// AppDB only uses the automation agent in headless mode
	appdbImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.AppDBImageUrl),
		env.ReadOrDefault(appDBAutomationAgentVersionEnv, "latest"))

	var volumeMounts []corev1.VolumeMount
	var volumes []corev1.Volume

	automationConfigVolume := statefulset.CreateVolumeFromSecret(clusterConfigVolumeName, opts.Name+"-config")
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

	if opts.PodVars != nil && opts.PodVars.SSLMMSCAConfigMap != "" {
		caCertVolume := statefulset.CreateVolumeFromConfigMap(caCertName, opts.PodVars.SSLMMSCAConfigMap)
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
		sharedDatabaseConfiguration(opts),
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
				container.WithEnvs(appdbContainerEnv(opts)...),
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

func appdbContainerEnv(opts DatabaseStatefulSetOptions) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:      podNamespaceEnv,
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			Name:  automationConfigMapEnv,
			Value: opts.Name + "-config",
		},
		{
			Name:  headlessAgentEnv,
			Value: "true",
		},
	}

	podVars := opts.PodVars
	// These env vars are required to configure Monitoring of the AppDB
	if podVars != nil && podVars.ProjectID != "" {
		envVars = append(envVars, envVarFromSecret(agentApiKeyEnv, agentApiKeySecretName(podVars.ProjectID), util.OmAgentApiKey))
		envVars = append(envVars, corev1.EnvVar{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: podVars.ProjectID,
		})
		envVars = append(envVars, corev1.EnvVar{
			Name:  util.ENV_VAR_USER,
			Value: podVars.User,
		})

		if podVars.SSLMMSCAConfigMap != "" {
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
