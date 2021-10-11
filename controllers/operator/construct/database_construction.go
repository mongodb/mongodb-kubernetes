package construct

import (
	"fmt"
	"path"
	"sort"
	"strconv"

	"github.com/10gen/ops-manager-kubernetes/pkg/tls"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/persistentvolumeclaim"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// Volume constants
	PvcNameDatabaseScripts = "database-scripts"
	PvcMountPathScripts    = "/opt/scripts"

	caCertMountPath = "/mongodb-automation/certs"
	// CaCertName is the name of the volume with the CA Cert
	CaCertName = "ca-cert-volume"
	// AgentCertMountPath defines where in the Pod the ca cert will be mounted.
	agentCertMountPath = "/mongodb-automation/" + util.AgentSecretName

	databaseLivenessProbeCommand  = "/opt/scripts/probe.sh"
	databaseReadinessProbeCommand = "/opt/scripts/readinessprobe"

	ControllerLabelName       = "controller"
	InitDatabaseContainerName = "mongodb-enterprise-init-database"

	// Database environment variable names
	InitDatabaseVersionEnv = "INIT_DATABASE_VERSION"
	DatabaseVersionEnv     = "DATABASE_VERSION"

	// PodAntiAffinityLabelKey defines the anti affinity rule label. The main rule is to spread entities inside one statefulset
	// (aka replicaset) to different locations, so pods having the same label shouldn't coexist on the node that has
	// the same topology key
	PodAntiAffinityLabelKey = "pod-anti-affinity"
)

// DatabaseStatefulSetOptions contains all of the different values that are variable between
// StatefulSets. Depending on which StatefulSet is being built, a number of these will be pre-set,
// while the remainder will be configurable via configuration functions which modify this type.
type DatabaseStatefulSetOptions struct {
	Replicas                int
	Name                    string
	ServiceName             string
	PodSpec                 *mdbv1.PodSpecWrapper
	PodVars                 *env.PodEnvVars
	CurrentAgentAuthMode    string
	CertificateHash         string
	ServicePort             int32
	Persistent              *bool
	OwnerReference          []metav1.OwnerReference
	AgentConfig             mdbv1.AgentConfig
	StatefulSetSpecOverride *appsv1.StatefulSetSpec
	Annotations             map[string]string
}

// databaseStatefulSetSource is an interface which provides all the required fields to fully construct
// a database StatefulSet.
type databaseStatefulSetSource interface {
	GetName() string
	GetNamespace() string
	GetSecurity() *mdbv1.Security
}

// StandaloneOptions returns a set of options which will configure a Standalone StatefulSet
func StandaloneOptions(additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			stsSpec = &appsv1.StatefulSetSpec{Template: *mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate}
		}

		opts := DatabaseStatefulSetOptions{
			Replicas:                1,
			Name:                    mdb.Name,
			ServiceName:             mdb.ServiceName(),
			PodSpec:                 newDefaultPodSpecWrapper(*mdb.Spec.PodSpec),
			ServicePort:             mdb.Spec.AdditionalMongodConfig.GetPortOrDefault(),
			Persistent:              mdb.Spec.Persistent,
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             mdb.Spec.Agent,
			StatefulSetSpecOverride: stsSpec,
		}

		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

// ReplicaSetOptions returns a set of options which will configure a ReplicaSet StatefulSet
func ReplicaSetOptions(additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			stsSpec = &appsv1.StatefulSetSpec{Template: *mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate}
		}

		opts := DatabaseStatefulSetOptions{
			Replicas:                scale.ReplicasThisReconciliation(&mdb),
			Name:                    mdb.Name,
			ServiceName:             mdb.ServiceName(),
			Annotations:             map[string]string{"type": "Replicaset"},
			PodSpec:                 newDefaultPodSpecWrapper(*mdb.Spec.PodSpec),
			ServicePort:             mdb.Spec.AdditionalMongodConfig.GetPortOrDefault(),
			Persistent:              mdb.Spec.Persistent,
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             mdb.Spec.Agent,
			StatefulSetSpecOverride: stsSpec,
		}
		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

// ShardOptions returns a set of options which will configure single Shard StatefulSet
func ShardOptions(shardNum int, additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if mdb.Spec.ShardPodSpec.PodTemplateWrapper.PodTemplate != nil {
			stsSpec = &appsv1.StatefulSetSpec{Template: *mdb.Spec.ShardPodSpec.PodTemplateWrapper.PodTemplate}
		}

		opts := DatabaseStatefulSetOptions{
			Name:                    mdb.ShardRsName(shardNum),
			ServiceName:             mdb.ShardServiceName(),
			PodSpec:                 newDefaultPodSpecWrapper(*mdb.Spec.ShardPodSpec),
			ServicePort:             mdb.Spec.ShardSpec.GetAdditionalMongodConfig().GetPortOrDefault(),
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             mdb.Spec.ShardSpec.GetAgentConfig(),
			Persistent:              mdb.Spec.Persistent,
			StatefulSetSpecOverride: stsSpec,
		}
		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

// ConfigServerOptions returns a set of options which will configure a Config Server StatefulSet
func ConfigServerOptions(additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if mdb.Spec.ConfigSrvPodSpec.PodTemplateWrapper.PodTemplate != nil {
			stsSpec = &appsv1.StatefulSetSpec{Template: *mdb.Spec.ConfigSrvPodSpec.PodTemplateWrapper.PodTemplate}
		}

		podSpecWrapper := newDefaultPodSpecWrapper(*mdb.Spec.ConfigSrvPodSpec)
		podSpecWrapper.Default.Persistence.SingleConfig.Storage = util.DefaultConfigSrvStorageSize
		opts := DatabaseStatefulSetOptions{
			Name:                    mdb.ConfigRsName(),
			ServiceName:             mdb.ConfigSrvServiceName(),
			PodSpec:                 podSpecWrapper,
			ServicePort:             mdb.Spec.ConfigSrvSpec.GetAdditionalMongodConfig().GetPortOrDefault(),
			Persistent:              mdb.Spec.Persistent,
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             mdb.Spec.ConfigSrvSpec.GetAgentConfig(),
			StatefulSetSpecOverride: stsSpec,
		}
		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

// MongosOptions returns a set of options which will configure a Mongos StatefulSet
func MongosOptions(additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if mdb.Spec.MongosPodSpec.PodTemplateWrapper.PodTemplate != nil {
			stsSpec = &appsv1.StatefulSetSpec{Template: *mdb.Spec.MongosPodSpec.PodTemplateWrapper.PodTemplate}
		}

		opts := DatabaseStatefulSetOptions{
			Name:                    mdb.MongosRsName(),
			ServiceName:             mdb.ServiceName(),
			PodSpec:                 newDefaultPodSpecWrapper(*mdb.Spec.MongosPodSpec),
			ServicePort:             mdb.Spec.MongosSpec.GetAdditionalMongodConfig().GetPortOrDefault(),
			Persistent:              util.BooleanRef(false),
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             mdb.Spec.MongosSpec.GetAgentConfig(),
			StatefulSetSpecOverride: stsSpec,
		}
		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

func DatabaseStatefulSet(mdb mdbv1.MongoDB, stsOptFunc func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions) appsv1.StatefulSet {
	stsOptions := stsOptFunc(mdb)
	dbSts := databaseStatefulSet(&mdb, &stsOptions)

	if len(stsOptions.Annotations) > 0 {
		dbSts.Annotations = stsOptions.Annotations
	}

	if stsOptions.StatefulSetSpecOverride != nil {
		dbSts.Spec = merge.StatefulSetSpecs(dbSts.Spec, *stsOptions.StatefulSetSpecOverride)
	}
	return dbSts
}

func databaseStatefulSet(mdb databaseStatefulSetSource, stsOpts *DatabaseStatefulSetOptions) appsv1.StatefulSet {
	templateFunc := buildMongoDBPodTemplateSpec(*stsOpts)
	return statefulset.New(buildDatabaseStatefulSetConfigurationFunction(mdb, templateFunc, *stsOpts))
}

// buildDatabaseStatefulSetConfigurationFunction returns the function that will modify the StatefulSet
func buildDatabaseStatefulSetConfigurationFunction(mdb databaseStatefulSetSource, podTemplateSpecFunc podtemplatespec.Modification, opts DatabaseStatefulSetOptions) statefulset.Modification {
	podLabels := map[string]string{
		appLabelKey:             opts.ServiceName,
		ControllerLabelName:     util.OperatorName,
		PodAntiAffinityLabelKey: opts.Name,
	}

	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)

	configureContainerSecurityContext := container.NOOP()
	configurePodSpecSecurityContext := podtemplatespec.NOOP()
	if !managedSecurityContext {
		configurePodSpecSecurityContext = podtemplatespec.WithSecurityContext(defaultPodSecurityContext())
		configureContainerSecurityContext = container.WithSecurityContext(DefaultSecurityContext())
	}

	configureImagePullSecrets := podtemplatespec.NOOP()
	name, found := env.Read(util.ImagePullSecrets)
	if found {
		configureImagePullSecrets = podtemplatespec.WithImagePullSecrets(name)
	}

	volumes, volumeMounts := getVolumesAndVolumeMounts(mdb, opts)

	var mounts []corev1.VolumeMount
	var pvcFuncs map[string]persistentvolumeclaim.Modification
	if opts.Persistent == nil || *opts.Persistent {
		pvcFuncs, mounts = buildPersistentVolumeClaimsFuncs(opts)
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
		appLabelKey: opts.ServiceName,
	}

	return statefulset.Apply(
		statefulset.WithLabels(ssLabels),
		statefulset.WithName(opts.Name),
		statefulset.WithNamespace(mdb.GetNamespace()),
		statefulset.WithMatchLabels(podLabels),
		statefulset.WithServiceName(opts.ServiceName),
		statefulset.WithReplicas(opts.Replicas),
		statefulset.WithOwnerReference(opts.OwnerReference),
		volumeClaimFuncs,
		statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
			podtemplatespec.WithAnnotations(defaultPodAnnotations(opts.CertificateHash)),
			podtemplatespec.WithAffinity(mdb.GetName(), PodAntiAffinityLabelKey, 100),
			podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
			podtemplatespec.WithPodLabels(podLabels),
			podtemplatespec.WithNodeAffinity(opts.PodSpec.NodeAffinityWrapper.NodeAffinity),
			podtemplatespec.WithPodAffinity(opts.PodSpec.PodAffinityWrapper.PodAffinity),
			podtemplatespec.WithContainerByIndex(0, sharedDatabaseContainerFunc(*opts.PodSpec, volumeMounts, configureContainerSecurityContext)),
			volumesFunc,
			configurePodSpecSecurityContext,
			configureImagePullSecrets,
			podTemplateSpecFunc,
		)),
	)
}

func buildPersistentVolumeClaimsFuncs(opts DatabaseStatefulSetOptions) (map[string]persistentvolumeclaim.Modification, []corev1.VolumeMount) {
	var claims map[string]persistentvolumeclaim.Modification
	var mounts []corev1.VolumeMount

	podSpec := opts.PodSpec
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
		claims, mounts = createClaimsAndMountsSingleModeFunc(config, opts)
	} else if podSpec.Persistence.MultipleConfig != nil {
		defaultConfig := *podSpec.Default.Persistence.MultipleConfig

		// Multiple claims, multiple mounts. No subpaths are used and everything is mounted to the root of directory
		claims, mounts = createClaimsAndMountsMultiModeFunc(opts.PodSpec.Persistence, defaultConfig)
	}
	return claims, mounts
}

func sharedDatabaseContainerFunc(podSpecWrapper mdbv1.PodSpecWrapper, volumeMounts []corev1.VolumeMount, configureContainerSecurityContext container.Modification) container.Modification {
	return container.Apply(
		container.WithResourceRequirements(buildRequirementsFromPodSpec(podSpecWrapper)),
		container.WithPorts([]corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}}),
		container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
		container.WithImage(env.ReadOrPanic(util.AutomationAgentImage)),
		container.WithVolumeMounts(volumeMounts),
		container.WithLivenessProbe(DatabaseLivenessProbe()),
		container.WithReadinessProbe(DatabaseReadinessProbe()),
		configureContainerSecurityContext,
	)
}

func getVolumesAndVolumeMounts(mdb databaseStatefulSetSource, databaseOpts DatabaseStatefulSetOptions) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumesToAdd []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	if mdb.GetSecurity() != nil {
		tlsConfig := mdb.GetSecurity().TLSConfig
		if mdb.GetSecurity().TLSConfig.IsEnabled() {
			secretName := mdb.GetSecurity().MemberCertificateSecretName(databaseOpts.Name)

			secretVolume := statefulset.CreateVolumeFromSecret(util.SecretVolumeName, secretName)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: util.SecretVolumeMountPath + "/certs",
				Name:      secretVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, secretVolume)
		}

		if tlsConfig.CA != "" {
			caVolume := statefulset.CreateVolumeFromConfigMap(tls.ConfigMapVolumeCAName, tlsConfig.CA)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: util.ConfigMapVolumeCAMountPath,
				Name:      caVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, caVolume)
		}
	}
	if databaseOpts.PodVars != nil && databaseOpts.PodVars.SSLMMSCAConfigMap != "" {
		caCertVolume := statefulset.CreateVolumeFromConfigMap(CaCertName, databaseOpts.PodVars.SSLMMSCAConfigMap)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: caCertMountPath,
			Name:      caCertVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, caCertVolume)
	}

	if mdb.GetSecurity() != nil {
		if mdb.GetSecurity().ShouldUseX509(databaseOpts.CurrentAgentAuthMode) || mdb.GetSecurity().ShouldUseClientCertificates() {
			agentSecretVolume := statefulset.CreateVolumeFromSecret(util.AgentSecretName, mdb.GetSecurity().AgentClientCertificateSecretName(mdb.GetName()).Name)
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				MountPath: agentCertMountPath,
				Name:      agentSecretVolume.Name,
				ReadOnly:  true,
			})
			volumesToAdd = append(volumesToAdd, agentSecretVolume)
		}
	}

	// add volume for x509 cert used in internal cluster authentication
	if mdb.GetSecurity().GetInternalClusterAuthenticationMode() == util.X509 {
		internalClusterAuthVolume := statefulset.CreateVolumeFromSecret(util.ClusterFileName, mdb.GetSecurity().InternalClusterAuthSecretName(databaseOpts.Name))
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
func buildMongoDBPodTemplateSpec(opts DatabaseStatefulSetOptions) podtemplatespec.Modification {
	// Database image version, should be a specific version to avoid using stale 'non-empty' versions (before versioning)
	databaseImageVersion := env.ReadOrDefault(DatabaseVersionEnv, "latest")
	databaseImageUrl := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.AutomationAgentImage), databaseImageVersion)
	// scripts volume is shared by the init container and the AppDB so the startup
	// script can be copied over
	scriptsVolume := statefulset.CreateVolumeFromEmptyDir("database-scripts")
	databaseScriptsVolumeMount := databaseScriptsVolumeMount(true)

	serviceAccountName := getServiceAccountName(opts)

	return podtemplatespec.Apply(
		sharedDatabaseConfiguration(opts),
		podtemplatespec.WithAnnotations(defaultPodAnnotations(opts.CertificateHash)),
		podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
		podtemplatespec.WithServiceAccount(serviceAccountName),
		podtemplatespec.WithVolume(scriptsVolume),
		podtemplatespec.WithInitContainerByIndex(0,
			buildDatabaseInitContainer(),
		),
		podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithName(util.DatabaseContainerName),
				container.WithImage(databaseImageUrl),
				container.WithEnvs(databaseEnvVars(opts)...),
				container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
				container.WithVolumeMounts([]corev1.VolumeMount{databaseScriptsVolumeMount}),
			),
		),
	)
}

// getServiceAccountName returns the serviceAccount to be used by the mongoDB pod,
// it uses the "serviceAccountName" specified in the podSpec of CR, if it's not specified returns
// the default serviceAccount name
func getServiceAccountName(opts DatabaseStatefulSetOptions) string {
	podSpec := opts.PodSpec

	if podSpec != nil && podSpec.PodTemplateWrapper.PodTemplate != nil {
		if podSpec.PodTemplateWrapper.PodTemplate.Spec.ServiceAccountName != "" {
			return podSpec.PodTemplateWrapper.PodTemplate.Spec.ServiceAccountName
		}
	}

	return util.MongoDBServiceAccount
}

// sharedDatabaseConfiguration is a function which applies all the shared configuration
// between the appDb and MongoDB resources
func sharedDatabaseConfiguration(opts DatabaseStatefulSetOptions) podtemplatespec.Modification {
	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)

	configurePodSpecSecurityContext := podtemplatespec.NOOP()
	if !managedSecurityContext {
		configurePodSpecSecurityContext = podtemplatespec.WithSecurityContext(defaultPodSecurityContext())
	}

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(DefaultSecurityContext())
	}

	pullSecretsConfigurationFunc := podtemplatespec.NOOP()
	if pullSecrets, ok := env.Read(util.ImagePullSecrets); ok {
		pullSecretsConfigurationFunc = podtemplatespec.WithImagePullSecrets(pullSecrets)
	}

	return podtemplatespec.Apply(
		podtemplatespec.WithPodLabels(defaultPodLabels(opts.ServiceName, opts.Name)),
		podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
		pullSecretsConfigurationFunc,
		configurePodSpecSecurityContext,
		podtemplatespec.WithAffinity(opts.Name, PodAntiAffinityLabelKey, 100),
		podtemplatespec.WithNodeAffinity(opts.PodSpec.NodeAffinityWrapper.NodeAffinity),
		podtemplatespec.WithPodAffinity(opts.PodSpec.PodAffinityWrapper.PodAffinity),
		podtemplatespec.WithTopologyKey(opts.PodSpec.GetTopologyKeyOrDefault(), 0),
		podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithResourceRequirements(buildRequirementsFromPodSpec(*opts.PodSpec)),
				container.WithPorts([]corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}}),
				container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
				container.WithLivenessProbe(DatabaseLivenessProbe()),
				container.WithEnvs(startupParametersToAgentFlag(opts.AgentConfig.StartupParameters)),
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
	for key, value := range defaultAgentParameters() {
		if _, ok := parameters[key]; !ok {
			// add the default parameter
			agentParams += "-" + key + "," + value + ","
		}
		// Skip as this has it will be set by custom flags
	}
	for key, value := range parameters {
		// Using comma as delimiter to split the string later
		// in the agentlauncher script
		agentParams += "-" + key + "," + value + ","
	}

	return corev1.EnvVar{Name: "AGENT_FLAGS", Value: agentParams}
}

func defaultAgentParameters() mdbv1.StartupParameters {
	return map[string]string{"logFile": "/var/log/mongodb-mms-automation/automation-agent.log"}
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
	version := env.ReadOrDefault(InitDatabaseVersionEnv, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.InitDatabaseImageUrlEnv), version)

	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(DefaultSecurityContext())
	}
	return container.Apply(
		container.WithName(InitDatabaseContainerName),
		container.WithImage(initContainerImageURL),
		configureContainerSecurityContext,
		container.WithVolumeMounts([]corev1.VolumeMount{
			databaseScriptsVolumeMount(false),
		}),
	)
}

func databaseEnvVars(opts DatabaseStatefulSetOptions) []corev1.EnvVar {
	podVars := opts.PodVars
	if podVars == nil {
		return []corev1.EnvVar{}
	}
	vars := []corev1.EnvVar{
		{
			Name:  util.ENV_VAR_BASE_URL,
			Value: podVars.BaseURL,
		},
		{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: podVars.ProjectID,
		},
		{
			Name:  util.ENV_VAR_USER,
			Value: podVars.User,
		},
		env.FromSecret(AgentApiKeyEnv, agentApiKeySecretName(podVars.ProjectID), util.OmAgentApiKey),
		{
			Name:  util.ENV_VAR_LOG_LEVEL,
			Value: string(podVars.LogLevel),
		},
	}

	if opts.PodVars.SSLRequireValidMMSServerCertificates {
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLRequireValidMMSCertificates,
				Value: strconv.FormatBool(opts.PodVars.SSLRequireValidMMSServerCertificates),
			},
		)
	}

	if opts.PodVars.SSLMMSCAConfigMap != "" {
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

func DatabaseLivenessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseLivenessProbeCommand}),
		probes.WithInitialDelaySeconds(60),
		probes.WithTimeoutSeconds(30),
		probes.WithPeriodSeconds(30),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(6),
	)
}
func DatabaseReadinessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseReadinessProbeCommand}),
		probes.WithFailureThreshold(4),
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

func defaultPodSecurityContext() corev1.PodSecurityContext {
	return corev1.PodSecurityContext{
		FSGroup: util.Int64Ref(util.FsGroup),
	}
}

func DefaultSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot: util.BooleanRef(true),
		RunAsUser:    util.Int64Ref(util.RunAsUser),
	}
}

// agentApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func agentApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

// TODO: temprorary duplication to avoid circular imports
func newDefaultPodSpecWrapper(podSpec mdbv1.MongoDbPodSpec) *mdbv1.PodSpecWrapper {
	return &mdbv1.PodSpecWrapper{
		MongoDbPodSpec: podSpec,
		Default:        newDefaultPodSpec(),
	}
}

func newDefaultPodSpec() mdbv1.MongoDbPodSpec {
	podSpecWrapper := mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetPodAntiAffinityTopologyKey(util.DefaultAntiAffinityTopologyKey).
		SetSinglePersistence(mdbv1.NewPersistenceBuilder(util.DefaultMongodStorageSize)).
		SetMultiplePersistence(mdbv1.NewPersistenceBuilder(util.DefaultMongodStorageSize),
			mdbv1.NewPersistenceBuilder(util.DefaultJournalStorageSize),
			mdbv1.NewPersistenceBuilder(util.DefaultLogsStorageSize)).
		Build()

	return podSpecWrapper.MongoDbPodSpec
}
