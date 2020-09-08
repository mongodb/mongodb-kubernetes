package construct

import (
	"fmt"
	"path"
	"sort"
	"strconv"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/persistentvolumeclaim"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Volume constants
	PvcNameDatabaseScripts = "database-scripts"
	PvcMountPathScripts = "/opt/scripts"

	caCertMountPath       = "/mongodb-automation/certs"
	configMapVolumeCAName = "secret-ca"
	// caCertName is the name of the volume with the CA Cert
	caCertName = "ca-cert-volume"
	// AgentCertMountPath defines where in the Pod the ca cert will be mounted.
	agentCertMountPath = "/mongodb-automation/" + util.AgentSecretName

	databaseLivenessProbeCommand  = "/opt/scripts/probe.sh"
	databaseReadinessProbeCommand = "/opt/scripts/readinessprobe"

	controllerLabelName = "controller"
	initDatabaseContainerName     = "mongodb-enterprise-init-database"

	// Database environment variable names
	initDatabaseVersionEnv  = "INIT_DATABASE_VERSION"
	databaseVersionEnv  = "DATABASE_VERSION"
)

type DatabaseBuilder interface {
	GetOwnerRefs() []metav1.OwnerReference
	GetName() string
	GetService() string
	GetNamespace() string
	GetReplicas() int
	GetCertificateHash() string
	GetPodSpec() *mdbv1.PodSpecWrapper
	GetSecurity() *mdbv1.Security
	IsPersistent() *bool
	GetCurrentAgentAuthMechanism() string
	GetBaseUrl() string
	GetProjectID() string
	GetUser() string
	SSLRequireValidMMSServerCertificates() bool
	GetSSLMMSCAConfigMap() string
	GetLogLevel() string
	GetStartupParameters() mdbv1.StartupParameters
}

// DatabaseStatefulSet fully constructs the database StatefulSet
func DatabaseStatefulSet(mdbBuilder DatabaseBuilder) appsv1.StatefulSet {
	templateFunc := buildMongoDBPodTemplateSpec(mdbBuilder)
	return statefulset.New(buildDatabaseStatefulSetConfigurationFunction(mdbBuilder, templateFunc))
}

// buildDatabaseStatefulSetConfigurationFunction returns the function that will modify the StatefulSet
func buildDatabaseStatefulSetConfigurationFunction(mdbBuilder DatabaseBuilder, podTemplateSpecFunc podtemplatespec.Modification) statefulset.Modification {
	podLabels := map[string]string{
		appLabelKey:             mdbBuilder.GetService(),
		controllerLabelName:     util.OperatorName,
		podAntiAffinityLabelKey: mdbBuilder.GetName(),
	}

	managedSecurityContext, _ := envutil.ReadBool(util.ManagedSecurityContextEnv)

	configurePodSpecSecurityContext := podtemplatespec.NOOP()
	if !managedSecurityContext {
		configurePodSpecSecurityContext = podtemplatespec.WithFsGroup(util.FsGroup)
	}

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(corev1.SecurityContext{
			RunAsUser:    util.Int64Ref(util.RunAsUser),
			RunAsNonRoot: util.BooleanRef(true),
		})
	}

	configureImagePullSecrets := podtemplatespec.NOOP()
	name, found := envutil.Read(util.ImagePullSecrets)
	if found {
		configureImagePullSecrets = withImagePullSecrets(name)
	}

	volumes, volumeMounts := getVolumesAndVolumeMounts(mdbBuilder)

	var mounts []corev1.VolumeMount
	var pvcFuncs map[string]persistentvolumeclaim.Modification
	if mdbBuilder.IsPersistent() == nil || *mdbBuilder.IsPersistent() {
		pvcFuncs, mounts = buildPersistentVolumeClaimsFuncs(mdbBuilder)
		volumeMounts = append(volumeMounts, mounts...)
	}

	volumesFunc := func(spec *corev1.PodTemplateSpec) {
		for _, v := range volumes {
			podtemplatespec.WithVolume(v)(spec)
		}
	}

	keys := make([]string, 0, len(pvcFuncs))
	for k := range pvcFuncs {
		keys = append(keys, k)
	}

	// ensure consistent order of PVCs
	sort.Strings(keys)

	volumeClaimFuncs := func(sts *appsv1.StatefulSet) {
		for _, name := range keys {
			statefulset.WithVolumeClaim(name, pvcFuncs[name])(sts)
		}
	}

	ssLabels := map[string]string{
		appLabelKey: mdbBuilder.GetService(),
	}

	return statefulset.Apply(
		statefulset.WithLabels(ssLabels),
		statefulset.WithName(mdbBuilder.GetName()),
		statefulset.WithNamespace(mdbBuilder.GetNamespace()),
		statefulset.WithMatchLabels(podLabels),
		statefulset.WithServiceName(mdbBuilder.GetService()),
		statefulset.WithReplicas(mdbBuilder.GetReplicas()),
		statefulset.WithOwnerReference(mdbBuilder.GetOwnerRefs()),
		volumeClaimFuncs,
		statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
			podtemplatespec.WithAnnotations(defaultPodAnnotations(mdbBuilder.GetCertificateHash())),
			podtemplatespec.WithAffinity(mdbBuilder.GetName(), podAntiAffinityLabelKey, 100),
			podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
			podtemplatespec.WithPodLabels(podLabels),
			podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
			podtemplatespec.WithNodeAffinity(mdbBuilder.GetPodSpec().NodeAffinity),
			podtemplatespec.WithPodAffinity(mdbBuilder.GetPodSpec().PodAffinity),
			podtemplatespec.WithContainerByIndex(0, sharedDatabaseContainerFunc(*mdbBuilder.GetPodSpec(), volumeMounts, configureContainerSecurityContext)),
			volumesFunc,
			configurePodSpecSecurityContext,
			configureImagePullSecrets,
			podTemplateSpecFunc,
		)),
	)
}

func buildPersistentVolumeClaimsFuncs(mdbBuilder DatabaseBuilder) (map[string]persistentvolumeclaim.Modification, []corev1.VolumeMount) {
	var claims map[string]persistentvolumeclaim.Modification
	var mounts []corev1.VolumeMount

	podSpec := mdbBuilder.GetPodSpec()
	// if persistence not set or if single one is set
	if podSpec.Persistence == nil ||
		(podSpec.Persistence.SingleConfig == nil && podSpec.Persistence.MultipleConfig == nil) ||
		podSpec.Persistence.SingleConfig != nil {
		var config *mdbv1.PersistenceConfig
		if podSpec.Persistence != nil && podSpec.Persistence.SingleConfig != nil {
			config = podSpec.Persistence.SingleConfig
		}
		// Single claim, multiple mounts using this claim. Note, that we use "subpaths" in the volume to mount to different
		// physical folders
		claims, mounts = createClaimsAndMountsSingleModeFunc(config, mdbBuilder)
	} else if podSpec.Persistence.MultipleConfig != nil {
		defaultConfig := *podSpec.Default.Persistence.MultipleConfig

		// Multiple claims, multiple mounts. No subpaths are used and everything is mounted to the root of directory
		claims, mounts = createClaimsAndMountsMultiModeFunc(mdbBuilder.GetPodSpec().Persistence, defaultConfig)
	}
	return claims, mounts
}

func sharedDatabaseContainerFunc(podSpecWrapper mdbv1.PodSpecWrapper, volumeMounts []corev1.VolumeMount, configureContainerSecurityContext container.Modification) container.Modification {
	return container.Apply(
		container.WithResourceRequirements(buildRequirementsFromPodSpec(podSpecWrapper)),
		container.WithPorts([]corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}}),
		container.WithImagePullPolicy(corev1.PullPolicy(envutil.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
		container.WithImage(envutil.ReadOrPanic(util.AutomationAgentImage)),
		withVolumeMounts(volumeMounts),
		container.WithLivenessProbe(databaseLivenessProbe()),
		container.WithReadinessProbe(databaseReadinessProbe()),
		configureContainerSecurityContext,
	)
}

func getVolumesAndVolumeMounts(mdbBuilder DatabaseBuilder) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumesToAdd []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	if mdbBuilder.GetSecurity() != nil {
		tlsConfig := mdbBuilder.GetSecurity().TLSConfig
		if mdbBuilder.GetSecurity().TLSConfig.IsEnabled() {
			var secretName string
			if tlsConfig.SecretRef.Name != "" {
				// From this location, the certificates will be used inplace
				secretName = tlsConfig.SecretRef.Name
			} else {
				// In this location the certificates will be linked -s into server.pem
				secretName = fmt.Sprintf("%s-cert", mdbBuilder.GetName())
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
			caVolume := statefulset.CreateVolumeFromConfigMap(configMapVolumeCAName, tlsConfig.CA)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: util.ConfigMapVolumeCAMountPath,
				Name:      caVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, caVolume)
		}
	}

	if mdbBuilder.GetSSLMMSCAConfigMap() != "" {
		caCertVolume := statefulset.CreateVolumeFromConfigMap(caCertName, mdbBuilder.GetSSLMMSCAConfigMap())
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: caCertMountPath,
			Name:      caCertVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, caCertVolume)
	}

	if mdbBuilder.GetSecurity() != nil {
		if mdbBuilder.GetSecurity().ShouldUseX509(mdbBuilder.GetCurrentAgentAuthMechanism()) {
			agentSecretVolume := statefulset.CreateVolumeFromSecret(util.AgentSecretName, util.AgentSecretName)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: agentCertMountPath,
				Name:      agentSecretVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, agentSecretVolume)
		}
	}

	// add volume for x509 cert used in internal cluster authentication
	if mdbBuilder.GetSecurity().GetInternalClusterAuthenticationMode() == util.X509 {
		internalClusterAuthVolume := statefulset.CreateVolumeFromSecret(util.ClusterFileName, toInternalClusterAuthName(mdbBuilder.GetName()))
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: util.InternalClusterAuthMountPath,
			Name:      internalClusterAuthVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, internalClusterAuthVolume)
	}

	return volumesToAdd, volumeMounts
}

// buildMongoDBPodTemplateSpec constructs the podTemplateSpec for the MongoDB resource
func buildMongoDBPodTemplateSpec(mdbBuilder DatabaseBuilder) podtemplatespec.Modification {
	// Database image version, should be a specific version to avoid using stale 'non-empty' versions (before versioning)
	databaseImageVersion := envutil.ReadOrDefault(databaseVersionEnv, "latest")
	databaseImageUrl := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.AutomationAgentImage), databaseImageVersion)
	// scripts volume is shared by the init container and the AppDB so the startup
	// script can be copied over
	scriptsVolume := statefulset.CreateVolumeFromEmptyDir("database-scripts")
	databaseScriptsVolumeMount := databaseScriptsVolumeMount(true)

	return podtemplatespec.Apply(
		sharedDatabaseConfiguration(mdbBuilder),
		podtemplatespec.WithAnnotations(defaultPodAnnotations(mdbBuilder.GetCertificateHash())),
		podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
		podtemplatespec.WithVolume(scriptsVolume),
		podtemplatespec.WithInitContainerByIndex(0,
			buildDatabaseInitContainer(),
		),
		podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithName(util.DatabaseContainerName),
				container.WithImage(databaseImageUrl),
				container.WithEnvs(databaseEnvVars(mdbBuilder)...),
				container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
				container.WithVolumeMounts([]corev1.VolumeMount{databaseScriptsVolumeMount}),
			),
		),
	)
}

// sharedDatabaseConfiguration is a function which applies all the shared configuration
// between the appDb and MongoDB resources
func sharedDatabaseConfiguration(mdbBuilder DatabaseBuilder) podtemplatespec.Modification {
	managedSecurityContext, _ := envutil.ReadBool(util.ManagedSecurityContextEnv)

	configurePodSpecSecurityContext := podtemplatespec.NOOP()
	if !managedSecurityContext {
		configurePodSpecSecurityContext = podtemplatespec.WithFsGroup(util.FsGroup)
	}

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(corev1.SecurityContext{
			RunAsUser:    util.Int64Ref(util.RunAsUser),
			RunAsNonRoot: util.BooleanRef(true),
		})
	}

	pullSecretsConfigurationFunc := podtemplatespec.NOOP()
	if pullSecrets, ok := envutil.Read(util.ImagePullSecrets); ok {
		pullSecretsConfigurationFunc = withImagePullSecrets(pullSecrets)
	}

	return podtemplatespec.Apply(
		podtemplatespec.WithPodLabels(defaultPodLabels(mdbBuilder.GetService(), mdbBuilder.GetName())),
		podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
		pullSecretsConfigurationFunc,
		configurePodSpecSecurityContext,
		podtemplatespec.WithAffinity(mdbBuilder.GetName(), podAntiAffinityLabelKey, 100),
		podtemplatespec.WithNodeAffinity(mdbBuilder.GetPodSpec().NodeAffinity),
		podtemplatespec.WithPodAffinity(mdbBuilder.GetPodSpec().PodAffinity),
		podtemplatespec.WithTopologyKey(mdbBuilder.GetPodSpec().GetTopologyKeyOrDefault(), 0),
		podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithResourceRequirements(buildRequirementsFromPodSpec(*mdbBuilder.GetPodSpec())),
				container.WithPorts([]corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}}),
				container.WithImagePullPolicy(corev1.PullPolicy(envutil.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
				container.WithLivenessProbe(databaseLivenessProbe()),
				container.WithEnvs(startupParametersToAgentFlag(mdbBuilder.GetStartupParameters())),
				configureContainerSecurityContext,
			),
		),
	)
}

// StartupParametersToAgentFlag takes a map representing key-value paris
// of startup parameters
// and concatenates them into a single string that is then
// returned as env variable AGENT_FLAGS
func startupParametersToAgentFlag(parameters mdbv1.StartupParameters) corev1.EnvVar {
	agentParams := ""
	for key, value := range parameters {
		agentParams += " -" + key + " " + value
	}
	return corev1.EnvVar{Name: "AGENT_FLAGS", Value: agentParams}
}

// databaseScriptsVolumeMount constructs the VolumeMount for the Database scripts
// this should be readonly for the Database, and not read only for the init container.
func databaseScriptsVolumeMount(readOnly bool) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      PvcNameDatabaseScripts,
		MountPath: PvcMountPathScripts,
		ReadOnly:  readOnly,
	}
}

// buildDatabaseInitContainer builds the container specification for mongodb-enterprise-init-database image
func buildDatabaseInitContainer() container.Modification {
	version := envutil.ReadOrDefault(initDatabaseVersionEnv, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.InitDatabaseImageUrlEnv), version)
	return container.Apply(
		container.WithName(initDatabaseContainerName),
		container.WithImage(initContainerImageURL),
		withVolumeMounts([]corev1.VolumeMount{
			databaseScriptsVolumeMount(false),
		}),
	)
}

func databaseEnvVars(databaseBuilder DatabaseBuilder) []corev1.EnvVar {
	vars := []corev1.EnvVar{
		{
			Name:  util.ENV_VAR_BASE_URL,
			Value: databaseBuilder.GetBaseUrl(),
		},
		{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: databaseBuilder.GetProjectID(),
		},
		{
			Name:  util.ENV_VAR_USER,
			Value: databaseBuilder.GetUser(),
		},
		envVarFromSecret(agentApiKeyEnv, agentApiKeySecretName(databaseBuilder.GetProjectID()), util.OmAgentApiKey),
		{
			Name:  util.ENV_VAR_LOG_LEVEL,
			Value: databaseBuilder.GetLogLevel(),
		},
	}

	if databaseBuilder.SSLRequireValidMMSServerCertificates() {
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLRequireValidMMSCertificates,
				Value: strconv.FormatBool(databaseBuilder.SSLRequireValidMMSServerCertificates()),
			},
		)
	}

	if databaseBuilder.GetSSLMMSCAConfigMap() != "" {
		// A custom CA has been provided, point the trusted CA to the location of custom CAs
		trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLTrustedMMSServerCertificate,
				Value: trustedCACertLocation,
			},
		)
	}

	return vars
}

// envVarFromSecret returns a corev1.EnvVar that is a reference to a secret with the field
// "secretKey" being used
func envVarFromSecret(envVarName, secretName, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
				Key: secretKey,
			},
		},
	}
}

func databaseLivenessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseLivenessProbeCommand}),
		probes.WithInitialDelaySeconds(60),
		withTimeoutSeconds(30),
		probes.WithPeriodSeconds(30),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(6),
	)
}
func databaseReadinessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseReadinessProbeCommand}),
		probes.WithFailureThreshold(240),
		probes.WithInitialDelaySeconds(5),
		probes.WithPeriodSeconds(5),
	)
}

func defaultPodAnnotations(certHash string) map[string]string {
	return map[string]string{
		// this annotation is necessary in order to trigger a pod restart
		// if the certificate secret is out of date. This happens if
		// existing certificates have been replaced/rotated/renewed.
		"certHash": certHash,
	}
}

// agentApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func agentApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

// toInternalClusterAuthName takes a hostname e.g. my-replica-set and converts
// it into the name of the secret which will hold the internal clusterFile
func toInternalClusterAuthName(name string) string {
	return fmt.Sprintf("%s-%s", name, util.ClusterFileName)
}
