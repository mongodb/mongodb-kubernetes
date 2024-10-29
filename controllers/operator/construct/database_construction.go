package construct

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/persistentvolumeclaim"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"

	mdbcv1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
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

	// AGENT_API_KEY secret path
	AgentAPIKeySecretPath = "/mongodb-automation/agent-api-key" //nolint
	AgentAPIKeyVolumeName = "agent-api-key"                     //nolint

	LogFileAutomationAgentEnv        = "MDB_LOG_FILE_AUTOMATION_AGENT"
	LogFileAutomationAgentVerboseEnv = "MDB_LOG_FILE_AUTOMATION_AGENT_VERBOSE"
	LogFileAutomationAgentStderrEnv  = "MDB_LOG_FILE_AUTOMATION_AGENT_STDERR"
	LogFileMongoDBAuditEnv           = "MDB_LOG_FILE_MONGODB_AUDIT"
	LogFileMongoDBEnv                = "MDB_LOG_FILE_MONGODB"
	LogFileAgentMonitoringEnv        = "MDB_LOG_FILE_MONITORING_AGENT"
	LogFileAgentBackupEnv            = "MDB_LOG_FILE_BACKUP_AGENT"
)

type StsType int

const (
	Undefined StsType = iota
	ReplicaSet
	Mongos
	Config
	Shard
	Standalone
	MultiReplicaSet
)

// DatabaseStatefulSetOptions contains all the different values that are variable between
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
	PrometheusTLSCertHash   string
	InternalClusterHash     string
	ServicePort             int32
	Persistent              *bool
	OwnerReference          []metav1.OwnerReference
	AgentConfig             *mdbv1.AgentConfig
	StatefulSetSpecOverride *appsv1.StatefulSetSpec
	StsType                 StsType
	AdditionalMongodConfig  *mdbv1.AdditionalMongodConfig
	MongoDBVersion          string
	AutomationAgentVersion  string

	Annotations map[string]string
	VaultConfig vault.VaultConfiguration
	ExtraEnvs   []corev1.EnvVar
	Labels      map[string]string

	// These fields are only relevant for multi-cluster
	MultiClusterMode bool // should always be "false" in single-cluster
	// This needs to be provided for the multi-cluster statefulsets as they contain a member index in the name.
	// The name override is used for naming the statefulset and the pod affinity label.
	// The certificate secrets and other dependencies named using the resource name will use the `Name` field.
	StatefulSetNameOverride       string // this needs to be overriden of the
	HostNameOverrideConfigmapName string
}

func (d DatabaseStatefulSetOptions) IsMongos() bool {
	return d.StsType == Mongos
}

func (d DatabaseStatefulSetOptions) GetStatefulSetName() string {
	if d.StatefulSetNameOverride != "" {
		return d.StatefulSetNameOverride
	}
	return d.Name
}

// databaseStatefulSetSource is an interface which provides all the required fields to fully construct
// a database StatefulSet.
type databaseStatefulSetSource interface {
	GetName() string
	GetNamespace() string

	GetSecurity() *mdbv1.Security

	GetPrometheus() *mdbcv1.Prometheus

	GetAnnotations() map[string]string
}

// StandaloneOptions returns a set of options which will configure a Standalone StatefulSet
func StandaloneOptions(additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			stsSpec = &appsv1.StatefulSetSpec{Template: *mdb.Spec.PodSpec.PodTemplateWrapper.PodTemplate}
		}

		opts := DatabaseStatefulSetOptions{
			MongoDBVersion:          mdb.Spec.Version,
			Replicas:                1,
			Name:                    mdb.Name,
			ServiceName:             mdb.ServiceName(),
			PodSpec:                 NewDefaultPodSpecWrapper(*mdb.Spec.PodSpec),
			ServicePort:             mdb.Spec.AdditionalMongodConfig.GetPortOrDefault(),
			Persistent:              mdb.Spec.Persistent,
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             &mdb.Spec.Agent,
			StatefulSetSpecOverride: stsSpec,
			MultiClusterMode:        mdb.Spec.IsMultiCluster(),
			StsType:                 Standalone,
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
			MongoDBVersion:          mdb.Spec.Version,
			Replicas:                scale.ReplicasThisReconciliation(&mdb),
			Name:                    mdb.Name,
			ServiceName:             mdb.ServiceName(),
			Annotations:             map[string]string{"type": "Replicaset"},
			PodSpec:                 NewDefaultPodSpecWrapper(*mdb.Spec.PodSpec),
			ServicePort:             mdb.Spec.AdditionalMongodConfig.GetPortOrDefault(),
			Persistent:              mdb.Spec.Persistent,
			OwnerReference:          kube.BaseOwnerReference(&mdb),
			AgentConfig:             &mdb.Spec.Agent,
			StatefulSetSpecOverride: stsSpec,
			Labels:                  mdb.Labels,
			MultiClusterMode:        mdb.Spec.IsMultiCluster(),
			StsType:                 ReplicaSet,
		}

		if mdb.Spec.DbCommonSpec.GetExternalDomain() != nil {
			opts.HostNameOverrideConfigmapName = mdb.GetHostNameOverrideConfigmapName()
		}

		for _, opt := range additionalOpts {
			opt(&opts)
		}

		return opts
	}
}

// shardedOptions group the shared logic for creating Shard, Config servers, and mongos options
func shardedOptions(cfg shardedOptionCfg, additionalOpts ...func(options *DatabaseStatefulSetOptions)) DatabaseStatefulSetOptions {
	clusterComponentSpec := cfg.componentSpec.GetClusterSpecItem(cfg.memberClusterName)
	statefulSetConfiguration := clusterComponentSpec.StatefulSetConfiguration
	var statefulSetSpecOverride *appsv1.StatefulSetSpec
	if statefulSetConfiguration != nil {
		statefulSetSpecOverride = &statefulSetConfiguration.SpecWrapper.Spec
	}

	podSpec := mdbv1.MongoDbPodSpec{}
	if clusterComponentSpec.PodSpec != nil && clusterComponentSpec.PodSpec.Persistence != nil {
		// Here, we explicitly ignore any other fields than Persistence
		// Although we still support the PodTemplate field in the Sharded Cluster CRD, when preparing the
		// ShardedClusterComponentSpec with functions prepareDesired[...]Configuration in the sharded controller, we
		// store anything related to the pod template in the clusterSpecList.StatefulSetConfiguration fields
		// The ShardOverrides.ClusterSpecList.PodSpec shouldn't contain anything relevant for the PodTemplate
		podSpec = mdbv1.MongoDbPodSpec{Persistence: clusterComponentSpec.PodSpec.Persistence}
	}

	opts := DatabaseStatefulSetOptions{
		MongoDBVersion:          cfg.mdb.Spec.Version,
		Name:                    cfg.rsName,
		ServiceName:             cfg.serviceName,
		PodSpec:                 NewDefaultPodSpecWrapper(podSpec),
		ServicePort:             cfg.componentSpec.GetAdditionalMongodConfig().GetPortOrDefault(),
		OwnerReference:          kube.BaseOwnerReference(&cfg.mdb),
		AgentConfig:             cfg.componentSpec.GetAgentConfig(),
		StatefulSetSpecOverride: statefulSetSpecOverride,
		Labels:                  cfg.mdb.Labels,
		MultiClusterMode:        cfg.mdb.Spec.IsMultiCluster(),
		Persistent:              cfg.persistent,
		StsType:                 cfg.stsType,
	}

	if cfg.mdb.Spec.IsMultiCluster() {
		opts.HostNameOverrideConfigmapName = cfg.mdb.GetHostNameOverrideConfigmapName()
	}

	for _, opt := range additionalOpts {
		opt(&opts)
	}

	return opts
}

type shardedOptionCfg struct {
	mdb               mdbv1.MongoDB
	componentSpec     *mdbv1.ShardedClusterComponentSpec
	rsName            string
	serviceName       string
	memberClusterName string
	stsType           StsType
	persistent        *bool
}

// ShardOptions returns a set of options which will configure single Shard StatefulSet
func ShardOptions(shardNum int, shardSpec *mdbv1.ShardedClusterComponentSpec, memberClusterName string, additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		cfg := shardedOptionCfg{
			mdb:               mdb,
			componentSpec:     shardSpec,
			rsName:            mdb.ShardRsName(shardNum),
			memberClusterName: memberClusterName,
			serviceName:       mdb.ShardServiceName(),
			stsType:           Shard,
			persistent:        mdb.Spec.Persistent,
		}

		return shardedOptions(cfg, additionalOpts...)
	}
}

// ConfigServerOptions returns a set of options which will configure a Config Server StatefulSet
func ConfigServerOptions(configSrvSpec *mdbv1.ShardedClusterComponentSpec, memberClusterName string, additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		cfg := shardedOptionCfg{
			mdb:               mdb,
			componentSpec:     configSrvSpec,
			rsName:            mdb.ConfigRsName(),
			memberClusterName: memberClusterName,
			serviceName:       mdb.ConfigSrvServiceName(),
			stsType:           Config,
			persistent:        mdb.Spec.Persistent,
		}

		return shardedOptions(cfg, additionalOpts...)
	}
}

// MongosOptions returns a set of options which will configure a Mongos StatefulSet
func MongosOptions(mongosSpec *mdbv1.ShardedClusterComponentSpec, memberClusterName string, additionalOpts ...func(options *DatabaseStatefulSetOptions)) func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
	return func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions {
		cfg := shardedOptionCfg{
			mdb:               mdb,
			componentSpec:     mongosSpec,
			rsName:            mdb.MongosRsName(),
			memberClusterName: memberClusterName,
			serviceName:       mdb.ServiceName(),
			stsType:           Mongos,
			persistent:        ptr.To(false),
		}

		return shardedOptions(cfg, additionalOpts...)
	}
}

func DatabaseStatefulSet(mdb mdbv1.MongoDB, stsOptFunc func(mdb mdbv1.MongoDB) DatabaseStatefulSetOptions, log *zap.SugaredLogger) appsv1.StatefulSet {
	stsOptions := stsOptFunc(mdb)
	dbSts := DatabaseStatefulSetHelper(&mdb, &stsOptions, log)

	if len(stsOptions.Annotations) > 0 {
		dbSts.Annotations = merge.StringToStringMap(dbSts.Annotations, stsOptions.Annotations)
	}

	if len(stsOptions.Labels) > 0 {
		dbSts.Labels = merge.StringToStringMap(dbSts.Labels, stsOptions.Labels)
	}

	if stsOptions.StatefulSetSpecOverride != nil {
		dbSts.Spec = merge.StatefulSetSpecs(dbSts.Spec, *stsOptions.StatefulSetSpecOverride)
	}

	return dbSts
}

func DatabaseStatefulSetHelper(mdb databaseStatefulSetSource, stsOpts *DatabaseStatefulSetOptions, log *zap.SugaredLogger) appsv1.StatefulSet {
	allSources := getAllMongoDBVolumeSources(mdb, *stsOpts, log)

	var extraEnvs []corev1.EnvVar
	for _, source := range allSources {
		if source.ShouldBeAdded() {
			extraEnvs = append(extraEnvs, source.GetEnvs()...)
		}
	}

	stsOpts.ExtraEnvs = extraEnvs

	templateFunc := buildMongoDBPodTemplateSpec(*stsOpts, mdb)
	return statefulset.New(buildDatabaseStatefulSetConfigurationFunction(mdb, templateFunc, *stsOpts, log))
}

// buildVaultDatabaseSecretsToInject fully constructs the DatabaseSecretsToInject required to
// convert to annotations to configure vault.
func buildVaultDatabaseSecretsToInject(mdb databaseStatefulSetSource, opts DatabaseStatefulSetOptions) vault.DatabaseSecretsToInject {
	secretsToInject := vault.DatabaseSecretsToInject{Config: opts.VaultConfig}

	if mdb.GetSecurity().ShouldUseX509(opts.CurrentAgentAuthMode) || mdb.GetSecurity().ShouldUseClientCertificates() {
		secretName := mdb.GetSecurity().AgentClientCertificateSecretName(mdb.GetName()).Name
		secretName = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)
		secretsToInject.AgentCerts = secretName
	}

	if mdb.GetSecurity().GetInternalClusterAuthenticationMode() == util.X509 {
		secretName := mdb.GetSecurity().InternalClusterAuthSecretName(opts.Name)
		secretName = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)
		secretsToInject.InternalClusterAuth = secretName
		secretsToInject.InternalClusterHash = opts.InternalClusterHash
	}

	// Enable prometheus injection
	prom := mdb.GetPrometheus()
	if prom != nil && prom.TLSSecretRef.Name != "" {
		// Only need to inject Prometheus TLS cert on Vault. Already done for Secret backend.
		secretsToInject.Prometheus = fmt.Sprintf("%s%s", prom.TLSSecretRef.Name, certs.OperatorGeneratedCertSuffix)
		secretsToInject.PrometheusTLSCertHash = opts.PrometheusTLSCertHash
	}

	// add vault specific annotations
	secretsToInject.AgentApiKey = agents.ApiKeySecretName(opts.PodVars.ProjectID)
	if mdb.GetSecurity().IsTLSEnabled() {
		secretName := mdb.GetSecurity().MemberCertificateSecretName(opts.Name)
		secretsToInject.MemberClusterAuth = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)
		secretsToInject.MemberClusterHash = opts.CertificateHash
	}
	return secretsToInject
}

// buildDatabaseStatefulSetConfigurationFunction returns the function that will modify the StatefulSet
func buildDatabaseStatefulSetConfigurationFunction(mdb databaseStatefulSetSource, podTemplateSpecFunc podtemplatespec.Modification, opts DatabaseStatefulSetOptions, log *zap.SugaredLogger) statefulset.Modification {
	podLabels := map[string]string{
		appLabelKey:             opts.ServiceName,
		ControllerLabelName:     util.OperatorName,
		PodAntiAffinityLabelKey: opts.Name,
	}

	configurePodSpecSecurityContext, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	configureImagePullSecrets := podtemplatespec.NOOP()
	name, found := env.Read(util.ImagePullSecrets)
	if found {
		configureImagePullSecrets = podtemplatespec.WithImagePullSecrets(name)
	}

	secretsToInject := buildVaultDatabaseSecretsToInject(mdb, opts)
	volumes, volumeMounts := getVolumesAndVolumeMounts(mdb, opts, secretsToInject.AgentCerts, secretsToInject.InternalClusterAuth)

	allSources := getAllMongoDBVolumeSources(mdb, opts, log)
	for _, source := range allSources {
		if source.ShouldBeAdded() {
			volumes = append(volumes, source.GetVolumes()...)
			volumeMounts = append(volumeMounts, source.GetVolumeMounts()...)
		}
	}

	var mounts []corev1.VolumeMount
	var pvcFuncs map[string]persistentvolumeclaim.Modification
	if opts.Persistent == nil || *opts.Persistent {
		pvcFuncs, mounts = buildPersistentVolumeClaimsFuncs(opts)
		volumeMounts = append(volumeMounts, mounts...)
	} else {
		volumes, volumeMounts = GetNonPersistentMongoDBVolumeMounts(volumes, volumeMounts)
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

	annotationFunc := statefulset.WithAnnotations(defaultPodAnnotations(opts.CertificateHash))
	podTemplateAnnotationFunc := podtemplatespec.NOOP()

	annotationFunc = statefulset.Apply(
		annotationFunc,
		statefulset.WithAnnotations(map[string]string{util.InternalCertAnnotationKey: opts.InternalClusterHash}),
	)

	if vault.IsVaultSecretBackend() {
		podTemplateAnnotationFunc = podtemplatespec.Apply(podTemplateAnnotationFunc, podtemplatespec.WithAnnotations(secretsToInject.DatabaseAnnotations(mdb.GetNamespace())))
	}

	stsName := opts.GetStatefulSetName()
	podAffinity := mdb.GetName()
	if opts.StatefulSetNameOverride != "" {
		stsName = opts.StatefulSetNameOverride
		podAffinity = opts.StatefulSetNameOverride
	}

	shareProcessNs := statefulset.NOOP()
	secondContainerModification := podtemplatespec.NOOP()

	if architectures.IsRunningStaticArchitecture(mdb.GetAnnotations()) {
		shareProcessNs = func(sts *appsv1.StatefulSet) {
			a := true
			sts.Spec.Template.Spec.ShareProcessNamespace = &a
		}
		secondContainerModification = podtemplatespec.WithContainerByIndex(1, container.WithVolumeMounts(volumeMounts))
	}

	return statefulset.Apply(
		statefulset.WithLabels(ssLabels),
		statefulset.WithName(stsName),
		statefulset.WithNamespace(mdb.GetNamespace()),
		statefulset.WithMatchLabels(podLabels),
		statefulset.WithServiceName(opts.ServiceName),
		statefulset.WithReplicas(opts.Replicas),
		statefulset.WithOwnerReference(opts.OwnerReference),
		annotationFunc,
		volumeClaimFuncs,
		shareProcessNs,
		statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
			podTemplateAnnotationFunc,
			podtemplatespec.WithAffinity(podAffinity, PodAntiAffinityLabelKey, 100),
			podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
			podtemplatespec.WithPodLabels(podLabels),
			podtemplatespec.WithContainerByIndex(0, sharedDatabaseContainerFunc(*opts.PodSpec, volumeMounts, configureContainerSecurityContext, opts.ServicePort, mdb.GetAnnotations(), opts.AutomationAgentVersion)),
			secondContainerModification,
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
		claims, mounts = createClaimsAndMountsMultiModeFunc(opts.PodSpec.Persistence, defaultConfig, opts.Labels)
	}
	return claims, mounts
}

func sharedDatabaseContainerFunc(podSpecWrapper mdbv1.PodSpecWrapper, volumeMounts []corev1.VolumeMount, configureContainerSecurityContext container.Modification, port int32, annotations map[string]string, automationAgentVersion string) container.Modification {
	var image container.Modification
	if architectures.IsRunningStaticArchitecture(annotations) {
		image = container.WithImage(ContainerImage(architectures.MdbAgentImageRepo, automationAgentVersion, func() string {
			return env.ReadOrDefault(architectures.MdbAgentImageRepo, "quay.io/mongodb/mongodb-agent-ubi")
		}))
	} else {
		image = container.WithImage(env.ReadOrPanic(util.NonStaticDatabaseEnterpriseImage))
	}

	return container.Apply(
		container.WithResourceRequirements(buildRequirementsFromPodSpec(podSpecWrapper)),
		container.WithPorts([]corev1.ContainerPort{{ContainerPort: port}}),
		container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
		container.WithVolumeMounts(volumeMounts),
		image,
		container.WithLivenessProbe(DatabaseLivenessProbe()),
		container.WithReadinessProbe(DatabaseReadinessProbe()),
		container.WithStartupProbe(DatabaseStartupProbe()),
		configureContainerSecurityContext,
	)
}

// getTLSPrometheusVolumeAndVolumeMount mounts Prometheus TLS Volumes from Secrets.
//
// These volumes will only be mounted when TLS has been enabled. They will
// contain the concatenated (PEM-format) certificates; which have been created
// by the Operator prior to this.
//
// Important: This function will not return Secret mounts in case of Vault backend!
//
// The Prometheus TLS Secret name is configured in:
//
// `spec.prometheus.tlsSecretRef.Name`
//
// The Secret will be mounted in:
// `/var/lib/mongodb-automation/secrets/prometheus`.
func getTLSPrometheusVolumeAndVolumeMount(prom *mdbcv1.Prometheus) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}

	if prom == nil || vault.IsVaultSecretBackend() {
		return volumes, volumeMounts
	}

	secretFunc := func(v *corev1.Volume) { v.Secret.Optional = util.BooleanRef(true) }
	// Name of the Secret (PEM-format) with the concatenation of the certificate and key.
	secretName := prom.TLSSecretRef.Name + certs.OperatorGeneratedCertSuffix

	secretVolume := statefulset.CreateVolumeFromSecret(util.PrometheusSecretVolumeName, secretName, secretFunc)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		MountPath: util.SecretVolumeMountPathPrometheus,
		Name:      secretVolume.Name,
	})
	volumes = append(volumes, secretVolume)

	return volumes, volumeMounts
}

// getAllMongoDBVolumeSources returns a slice of  MongoDBVolumeSource. These are used to determine which volumes
// and volume mounts should be added to the StatefulSet.
func getAllMongoDBVolumeSources(mdb databaseStatefulSetSource, databaseOpts DatabaseStatefulSetOptions, log *zap.SugaredLogger) []MongoDBVolumeSource {
	caVolume := &caVolumeSource{
		opts:   databaseOpts,
		logger: log,
	}
	tlsVolume := &tlsVolumeSource{
		security:     mdb.GetSecurity(),
		databaseOpts: databaseOpts,
		logger:       log,
	}

	var allVolumeSources []MongoDBVolumeSource
	allVolumeSources = append(allVolumeSources, caVolume)
	allVolumeSources = append(allVolumeSources, tlsVolume)

	return allVolumeSources
}

// getVolumesAndVolumeMounts returns all volumes and mounts required for the StatefulSet.
func getVolumesAndVolumeMounts(mdb databaseStatefulSetSource, databaseOpts DatabaseStatefulSetOptions, agentCertsSecretName, internalClusterAuthSecretName string) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumesToAdd []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	prometheusVolumes, prometheusVolumeMounts := getTLSPrometheusVolumeAndVolumeMount(mdb.GetPrometheus())

	volumesToAdd = append(volumesToAdd, prometheusVolumes...)
	volumeMounts = append(volumeMounts, prometheusVolumeMounts...)

	if !vault.IsVaultSecretBackend() && mdb.GetSecurity().ShouldUseX509(databaseOpts.CurrentAgentAuthMode) || mdb.GetSecurity().ShouldUseClientCertificates() {
		agentSecretVolume := statefulset.CreateVolumeFromSecret(util.AgentSecretName, agentCertsSecretName)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: agentCertMountPath,
			Name:      agentSecretVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, agentSecretVolume)
	}

	// add volume for x509 cert used in internal cluster authentication
	if !vault.IsVaultSecretBackend() && mdb.GetSecurity().GetInternalClusterAuthenticationMode() == util.X509 {
		internalClusterAuthVolume := statefulset.CreateVolumeFromSecret(util.ClusterFileName, internalClusterAuthSecretName)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: util.InternalClusterAuthMountPath,
			Name:      internalClusterAuthVolume.Name,
			ReadOnly:  true,
		})
		volumesToAdd = append(volumesToAdd, internalClusterAuthVolume)
	}

	if !vault.IsVaultSecretBackend() {
		volumesToAdd = append(volumesToAdd, statefulset.CreateVolumeFromSecret(AgentAPIKeyVolumeName, agents.ApiKeySecretName(databaseOpts.PodVars.ProjectID)))
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(AgentAPIKeyVolumeName, AgentAPIKeySecretPath))
	}

	volumesToAdd, volumeMounts = GetNonPersistentAgentVolumeMounts(volumesToAdd, volumeMounts)

	return volumesToAdd, volumeMounts
}

// buildMongoDBPodTemplateSpec constructs the podTemplateSpec for the MongoDB resource
func buildMongoDBPodTemplateSpec(opts DatabaseStatefulSetOptions, mdb databaseStatefulSetSource) podtemplatespec.Modification {
	// Database image version should be a specific version to avoid using stale 'non-empty' versions (before versioning)
	databaseImageVersion := env.ReadOrDefault(DatabaseVersionEnv, "latest")
	databaseNonStaticImage := ContainerImage(util.NonStaticDatabaseEnterpriseImage, databaseImageVersion, nil)

	// scripts volume is shared by the init container and the AppDB, so the startup
	// script can be copied over
	scriptsVolume := statefulset.CreateVolumeFromEmptyDir("database-scripts")
	databaseScriptsVolumeMount := databaseScriptsVolumeMount(true)

	volumes := []corev1.Volume{scriptsVolume}
	volumeMounts := []corev1.VolumeMount{databaseScriptsVolumeMount}

	initContainerModifications := []func(*corev1.Container){buildDatabaseInitContainer()}
	databaseContainerModifications := []func(*corev1.Container){container.Apply(
		container.WithName(util.DatabaseContainerName),
		container.WithImage(databaseNonStaticImage),
		container.WithEnvs(databaseEnvVars(opts)...),
		container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
		container.WithVolumeMounts(volumeMounts),
	)}

	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	staticContainerMongodContainerModification := podtemplatespec.NOOP()
	if architectures.IsRunningStaticArchitecture(mdb.GetAnnotations()) {
		// we don't use initContainers therefore, we reset it here
		initContainerModifications = []func(*corev1.Container){}
		mongodModification := []func(*corev1.Container){container.Apply(
			container.WithName(util.DatabaseContainerName),
			container.WithArgs([]string{""}),
			container.WithImage(getOfficialImage(opts.MongoDBVersion, mdb.GetAnnotations())),
			container.WithEnvs(databaseEnvVars(opts)...),
			container.WithCommand([]string{"bash", "-c", "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"}),
			containerSecurityContext,
		)}
		staticContainerMongodContainerModification = podtemplatespec.WithContainerByIndex(1, mongodModification...)

		agentImage := ContainerImage(architectures.MdbAgentImageRepo, opts.AutomationAgentVersion, func() string {
			return env.ReadOrDefault(architectures.MdbAgentImageRepo, "quay.io/mongodb/mongodb-agent-ubi")
		})

		// We are not setting the database-scripts volume on purpose,
		// since we don't need to copy things from the init container over.
		databaseContainerModifications = []func(*corev1.Container){container.Apply(
			container.WithName(util.AgentContainerName),
			container.WithImage(agentImage),
			container.WithEnvs(databaseEnvVars(opts)...),
			containerSecurityContext,
		)}
	}

	if opts.HostNameOverrideConfigmapName != "" {
		volumes = append(volumes, statefulset.CreateVolumeFromConfigMap(opts.HostNameOverrideConfigmapName, opts.HostNameOverrideConfigmapName))
		modification := container.WithVolumeMounts([]corev1.VolumeMount{
			{
				Name:      opts.HostNameOverrideConfigmapName,
				MountPath: "/opt/scripts/config",
			},
		})

		// we only need to add the volume modification if we actually use an init container
		if len(initContainerModifications) > 0 {
			initContainerModifications = append(initContainerModifications, modification)
		}

		databaseContainerModifications = append(databaseContainerModifications, modification)
	}

	serviceAccountName := getServiceAccountName(opts)

	mods := []podtemplatespec.Modification{
		sharedDatabaseConfiguration(opts, mdb),
		podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
		podtemplatespec.WithServiceAccount(serviceAccountName),
		podtemplatespec.WithVolumes(volumes),
		podtemplatespec.WithContainerByIndex(0, databaseContainerModifications...),
		staticContainerMongodContainerModification,
	}

	if len(initContainerModifications) > 0 {
		mods = append(mods, podtemplatespec.WithInitContainerByIndex(0, initContainerModifications...))
	}

	return podtemplatespec.Apply(mods...)
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
func sharedDatabaseConfiguration(opts DatabaseStatefulSetOptions, mdb databaseStatefulSetSource) podtemplatespec.Modification {
	configurePodSpecSecurityContext, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	pullSecretsConfigurationFunc := podtemplatespec.NOOP()
	if pullSecrets, ok := env.Read(util.ImagePullSecrets); ok {
		pullSecretsConfigurationFunc = podtemplatespec.WithImagePullSecrets(pullSecrets)
	}

	agentModification := podtemplatespec.WithContainerByIndex(0,
		container.Apply(
			container.WithResourceRequirements(buildRequirementsFromPodSpec(*opts.PodSpec)),
			container.WithPorts([]corev1.ContainerPort{{ContainerPort: opts.ServicePort}}),
			container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
			container.WithLivenessProbe(DatabaseLivenessProbe()),
			container.WithEnvs(startupParametersToAgentFlag(opts.AgentConfig.StartupParameters)),
			container.WithEnvs(logConfigurationToEnvVars(opts.AgentConfig.StartupParameters, opts.AdditionalMongodConfig)...),
			container.WithEnvs(readinessEnvironmentVariablesToEnvVars(opts.AgentConfig.ReadinessProbe.EnvironmentVariables)...),
			configureContainerSecurityContext,
		),
	)

	staticMongodModification := podtemplatespec.NOOP()
	if architectures.IsRunningStaticArchitecture(mdb.GetAnnotations()) {
		// The mongod
		staticMongodModification = podtemplatespec.WithContainerByIndex(1,
			container.Apply(
				container.WithArgs([]string{"tail -F -n0 \"${MDB_LOG_FILE_MONGODB}\""}),
				container.WithResourceRequirements(buildRequirementsFromPodSpec(*opts.PodSpec)),
				container.WithPorts([]corev1.ContainerPort{{ContainerPort: opts.ServicePort}}),
				container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
				container.WithEnvs(startupParametersToAgentFlag(opts.AgentConfig.StartupParameters)),
				container.WithEnvs(logConfigurationToEnvVars(opts.AgentConfig.StartupParameters, opts.AdditionalMongodConfig)...),
				configureContainerSecurityContext,
			),
		)
		agentModification = podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
				container.WithLivenessProbe(DatabaseLivenessProbe()),
				container.WithEnvs(startupParametersToAgentFlag(opts.AgentConfig.StartupParameters)),
				container.WithEnvs(logConfigurationToEnvVars(opts.AgentConfig.StartupParameters, opts.AdditionalMongodConfig)...),
				container.WithEnvs(staticContainersEnvVars(opts)...),
				container.WithEnvs(readinessEnvironmentVariablesToEnvVars(opts.AgentConfig.ReadinessProbe.EnvironmentVariables)...),
				container.WithArgs([]string{}),
				container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
				configureContainerSecurityContext,
			),
		)
	}

	return podtemplatespec.Apply(
		podtemplatespec.WithPodLabels(defaultPodLabels(opts.ServiceName, opts.Name)),
		podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
		pullSecretsConfigurationFunc,
		configurePodSpecSecurityContext,
		podtemplatespec.WithAffinity(opts.Name, PodAntiAffinityLabelKey, 100),
		podtemplatespec.WithTopologyKey(opts.PodSpec.GetTopologyKeyOrDefault(), 0),
		// The Agent
		agentModification,
		// AgentLoggingMongodConfig if static container
		staticMongodModification,
	)
}

// StartupParametersToAgentFlag takes a map representing key-value pairs
// of startup parameters
// and concatenates them into a single string that is then
// returned as env variable AGENT_FLAGS
func startupParametersToAgentFlag(parameters mdbv1.StartupParameters) corev1.EnvVar {
	agentParams := ""
	finalParameters := mdbv1.StartupParameters{}
	// add default parameters if not already set
	for key, value := range defaultAgentParameters() {
		if _, ok := parameters[key]; !ok {
			// add the default parameter
			finalParameters[key] = value
		}
	}
	for key, value := range parameters {
		finalParameters[key] = value
	}
	// sort the parameters by key
	keys := make([]string, 0, len(finalParameters))
	for k := range finalParameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// Using comma as delimiter to split the string later
		// in the agentlauncher script
		agentParams += "-" + key + "=" + finalParameters[key] + ","
	}

	return corev1.EnvVar{Name: "AGENT_FLAGS", Value: agentParams}
}

// readinessEnvironmentVariablesToEnvVars returns the environment variables to bet set in the readinessProbe container
func readinessEnvironmentVariablesToEnvVars(parameters mdbv1.EnvironmentVariables) []corev1.EnvVar {
	var finalParameters []corev1.EnvVar
	for key, value := range parameters {
		finalParameters = append(finalParameters, corev1.EnvVar{
			Name:  key,
			Value: value,
		})
	}
	sort.SliceStable(finalParameters, func(i, j int) bool {
		return finalParameters[i].Name > finalParameters[j].Name
	})

	return finalParameters
}

func defaultAgentParameters() mdbv1.StartupParameters {
	return map[string]string{"logFile": path.Join(util.PvcMountPathLogs, "automation-agent.log")}
}

func logConfigurationToEnvVars(parameters mdbv1.StartupParameters, additionalMongodConfig *mdbv1.AdditionalMongodConfig) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	envVars = append(envVars, getAutomationLogEnvVars(parameters)...)
	envVars = append(envVars, getAuditLogEnvVar(additionalMongodConfig))

	// the following are hardcoded log files where we don't support changing the names
	envVars = append(envVars, corev1.EnvVar{Name: LogFileMongoDBEnv, Value: path.Join(util.PvcMountPathLogs, "mongodb.log")})
	envVars = append(envVars, corev1.EnvVar{Name: LogFileAgentMonitoringEnv, Value: path.Join(util.PvcMountPathLogs, "monitoring-agent.log")})
	envVars = append(envVars, corev1.EnvVar{Name: LogFileAgentBackupEnv, Value: path.Join(util.PvcMountPathLogs, "backup-agent.log")})

	return envVars
}

func staticContainersEnvVars(opts DatabaseStatefulSetOptions) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	if architectures.IsRunningStaticArchitecture(opts.Annotations) {
		envVars = append(envVars, corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"})
	}
	return envVars
}

func getAuditLogEnvVar(additionalMongodConfig *mdbv1.AdditionalMongodConfig) corev1.EnvVar {
	auditLogFile := path.Join(util.PvcMountPathLogs, "mongodb-audit.log")
	if additionalMongodConfig != nil {
		if auditLogMap := maputil.ReadMapValueAsMap(additionalMongodConfig.ToMap(), "auditLog"); auditLogMap != nil {
			auditLogDestination := maputil.ReadMapValueAsString(auditLogMap, "destination")
			auditLogFilePath := maputil.ReadMapValueAsString(auditLogMap, "path")
			if auditLogDestination == "file" && len(auditLogFile) > 0 {
				auditLogFile = auditLogFilePath
			}
		}
	}

	return corev1.EnvVar{Name: LogFileMongoDBAuditEnv, Value: auditLogFile}
}

func getAutomationLogEnvVars(parameters mdbv1.StartupParameters) []corev1.EnvVar {
	automationLogFile := path.Join(util.PvcMountPathLogs, "automation-agent.log")
	if logFileValue, ok := parameters["logFile"]; ok && len(logFileValue) > 0 {
		automationLogFile = logFileValue
	}

	logFileDir, logFileName := path.Split(automationLogFile)
	logFileExt := path.Ext(logFileName)
	logFileWithoutExt := logFileName[0 : len(logFileName)-len(logFileExt)]

	verboseLogFile := fmt.Sprintf("%s%s-verbose%s", logFileDir, logFileWithoutExt, logFileExt)
	stderrLogFile := fmt.Sprintf("%s%s-stderr%s", logFileDir, logFileWithoutExt, logFileExt)
	return []corev1.EnvVar{
		{Name: LogFileAutomationAgentVerboseEnv, Value: verboseLogFile},
		{Name: LogFileAutomationAgentStderrEnv, Value: stderrLogFile},
		{Name: LogFileAutomationAgentEnv, Value: automationLogFile},
	}
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
	initContainerImageURL := ContainerImage(util.InitDatabaseImageUrlEnv, version, nil)

	_, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	return container.Apply(
		container.WithName(InitDatabaseContainerName),
		container.WithImage(initContainerImageURL),
		container.WithVolumeMounts([]corev1.VolumeMount{
			databaseScriptsVolumeMount(false),
		}),
		configureContainerSecurityContext,
	)
}

func databaseEnvVars(opts DatabaseStatefulSetOptions) []corev1.EnvVar {
	podVars := opts.PodVars
	if podVars == nil {
		return []corev1.EnvVar{}
	}
	vars := []corev1.EnvVar{
		{
			Name:  util.EnvVarLogLevel,
			Value: podVars.LogLevel,
		},
		{
			Name:  util.EnvVarBaseUrl,
			Value: podVars.BaseURL,
		},
		{
			Name:  util.EnvVarProjectId,
			Value: podVars.ProjectID,
		},
		{
			Name:  util.EnvVarUser,
			Value: podVars.User,
		},
		{
			Name:  util.EnvVarMultiClusterMode,
			Value: fmt.Sprintf("%t", opts.MultiClusterMode),
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

	// This is only used for debugging
	if useDebugAgent := os.Getenv(util.EnvVarDebug); useDebugAgent != "" {
		zap.S().Debugf("running the agent in debug mode")
		vars = append(vars, corev1.EnvVar{Name: util.EnvVarDebug, Value: useDebugAgent})
	}

	// This is only used for debugging
	if agentVersion := os.Getenv(util.EnvVarAgentVersion); agentVersion != "" {
		zap.S().Debugf("using a custom agent version: %s", agentVersion)
		vars = append(vars, corev1.EnvVar{Name: util.EnvVarAgentVersion, Value: agentVersion})
	}

	// append any additional env vars specified.
	vars = append(vars, opts.ExtraEnvs...)

	return vars
}

func DatabaseLivenessProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseLivenessProbeCommand}),
		probes.WithInitialDelaySeconds(10),
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

func DatabaseStartupProbe() probes.Modification {
	return probes.Apply(
		probes.WithExecCommand([]string{databaseLivenessProbeCommand}),
		probes.WithInitialDelaySeconds(1),
		probes.WithTimeoutSeconds(30),
		probes.WithPeriodSeconds(20),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(10),
	)
}

func defaultPodAnnotations(certHash string) map[string]string {
	return map[string]string{
		// This annotation is necessary to trigger a pod restart
		// if the certificate secret is out of date. This happens if
		// existing certificates have been replaced/rotated/renewed.
		certs.CertHashAnnotationKey: certHash,
	}
}

// TODO: temprorary duplication to avoid circular imports
func NewDefaultPodSpecWrapper(podSpec mdbv1.MongoDbPodSpec) *mdbv1.PodSpecWrapper {
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

// GetNonPersistentMongoDBVolumeMounts returns two arrays of non-persistent, empty volumes and corresponding mounts for the database container.
func GetNonPersistentMongoDBVolumeMounts(volumes []corev1.Volume, volumeMounts []corev1.VolumeMount) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes = append(volumes, statefulset.CreateVolumeFromEmptyDir(util.PvcNameData))

	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData, statefulset.WithSubPath(util.PvcNameData)))
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal)))
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathLogs, statefulset.WithSubPath(util.PvcNameLogs)))

	return volumes, volumeMounts
}

// GetNonPersistentAgentVolumeMounts returns two arrays of non-persistent, empty volumes and corresponding mounts for the Agent container.
func GetNonPersistentAgentVolumeMounts(volumes []corev1.Volume, volumeMounts []corev1.VolumeMount) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes = append(volumes, statefulset.CreateVolumeFromEmptyDir(util.PvMms))

	// The agent reads and writes into its own directory. It also contains a subdirectory called downloads.
	// This one is published by the Dockerfile
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.PvMms, util.PvcMmsMountPath, statefulset.WithSubPath(util.PvcMms)))

	// Runtime data for MMS
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.PvMms, util.PvcMmsHomeMountPath, statefulset.WithSubPath(util.PvcMmsHome)))

	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.PvMms, util.PvcMountPathTmp, statefulset.WithSubPath(util.PvcNameTmp)))
	return volumes, volumeMounts
}

// replaceImageTagOrDigestToTag returns the image with the tag or digest replaced to a given version
func replaceImageTagOrDigestToTag(image string, newVersion string) string {
	// example: quay.io/mongodb/mongodb-agent@sha256:6a82abae27c1ba1133f3eefaad71ea318f8fa87cc57fe9355d6b5b817ff97f1a
	if strings.Contains(image, "sha256:") {
		imageSplit := strings.Split(image, "@")
		imageSplit[len(imageSplit)-1] = newVersion
		return strings.Join(imageSplit, ":")
	} else {
		// examples:
		//  - quay.io/mongodb/mongodb-agent:1234-567
		//  - private-registry.local:3000/mongodb/mongodb-agent:1234-567
		//  - mongodb
		idx := strings.IndexRune(image, '/')
		// If there is no domain separator in the image string or the segment before the slash does not contain
		// a '.' or ':' and is not 'localhost' to indicate that the segment is a host, assume the image will be pulled from
		// docker.io and the whole string represents the image.
		if idx == -1 || (!strings.ContainsAny(image[:idx], ".:") && image[:idx] != "localhost") {
			return fmt.Sprintf("%s:%s", image[:strings.LastIndex(image, ":")], newVersion)
		}

		host := image[:idx]
		imagePath := image[idx+1:]

		// If there is a ':' in the image path we can safely assume that it is a version separator.
		if strings.Contains(imagePath, ":") {
			imagePath = imagePath[:strings.LastIndex(imagePath, ":")]
		}
		return fmt.Sprintf("%s/%s:%s", host, imagePath, newVersion)
	}
}

// ContainerImage builds container image using image environment variable imageURLEnv and version.
// It handles image digests when running in disconnected environment in OpenShift, where images
// are referenced by sha256 digest instead of tags.
// It works by convention by looking up RELATED_IMAGE_{imageURLEnv}_{version_underscored}.
// RELATED_IMAGE_* env variables are set in Helm chart for OpenShift.
// In case there is no RELATED_IMAGE defined it replaces digest or tag to version.
func ContainerImage(imageURLEnv string, version string, retrieveImageURL func() string) string {
	versionUnderscored := strings.ReplaceAll(version, ".", "_")
	versionUnderscored = strings.ReplaceAll(versionUnderscored, "-", "_")
	relatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_%s", imageURLEnv, versionUnderscored)

	if relatedImage := os.Getenv(relatedImageEnv); relatedImage != "" {
		return relatedImage
	}

	var imageURL string
	if retrieveImageURL != nil {
		imageURL = retrieveImageURL()
	} else {
		imageURL = os.Getenv(imageURLEnv)
	}

	if strings.Contains(imageURL, ":") {
		// here imageURL is not a host only but also with version or digest
		// in that case we need to replace the version/digest.
		// This is case with AGENT_IMAGE env variable which is provided as a full URL with version
		// and not as a pair of host and version.
		// In case AGENT_IMAGE is full URL with digest it will be replaced to given tag version,
		// but most probably if it has digest, there is also RELATED_IMAGE defined, which will be picked up first.
		return replaceImageTagOrDigestToTag(imageURL, version)
	}

	return fmt.Sprintf("%s:%s", imageURL, version)
}
