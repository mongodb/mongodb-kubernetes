package construct

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
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

	// AGENT_API_KEY secret path
	AgentAPIKeySecretPath = "/mongodb-automation/agent-api-key"
	AgentAPIKeyVolumeName = "agent-api-key"
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
	PrometheusTLSCertHash   string
	InternalClusterHash     string
	ServicePort             int32
	Persistent              *bool
	OwnerReference          []metav1.OwnerReference
	AgentConfig             mdbv1.AgentConfig
	StatefulSetSpecOverride *appsv1.StatefulSetSpec
	Annotations             map[string]string
	VaultConfig             vault.VaultConfiguration
	ExtraEnvs               []corev1.EnvVar
	Labels                  map[string]string
	CertSecretTypes         CertSecretTypesMapping
}

type CertSecretTypesMapping struct {
	// CertSecretIsTLSType is a map between certificate names and booleans that tell us
	// whether the given certificate is formatted with the new design (tls.crt and tls.key entries)
	// rather than the old one (concatenated PEM file)
	certSecretIsTLSType map[string]bool
}

func (c *CertSecretTypesMapping) SetCertType(certName string, isTLS bool) {
	if c.certSecretIsTLSType == nil {
		c.certSecretIsTLSType = map[string]bool{}
	}
	c.certSecretIsTLSType[certName] = isTLS
}

func (c CertSecretTypesMapping) IsCertTLSType(certName string) bool {
	isTLS, ok := c.certSecretIsTLSType[certName]
	return ok && isTLS
}

func (c CertSecretTypesMapping) IsTLSTypeOrUndefined(certName string) bool {
	isTLS, ok := c.certSecretIsTLSType[certName]
	return !ok || isTLS
}

func GetpodEnvOptions() func(options *DatabaseStatefulSetOptions) {
	return func(options *DatabaseStatefulSetOptions) {
		options.PodVars = &env.PodEnvVars{ProjectID: "abcd"}

	}
}

// databaseStatefulSetSource is an interface which provides all the required fields to fully construct
// a database StatefulSet.
type databaseStatefulSetSource interface {
	GetName() string
	GetNamespace() string

	GetSecurity() *mdbv1.Security

	GetPrometheus() *mdbcv1.Prometheus
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
			Labels:                  mdb.Labels,
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
			Labels:                  mdb.Labels,
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
			Labels:                  mdb.Labels,
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
			Labels:                  mdb.Labels,
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

func databaseStatefulSet(mdb databaseStatefulSetSource, stsOpts *DatabaseStatefulSetOptions) appsv1.StatefulSet {
	allSources := getAllMongoDBVolumeSources(mdb, *stsOpts)

	var extraEnvs []corev1.EnvVar
	for _, source := range allSources {
		if source.ShouldBeAdded() {
			extraEnvs = append(extraEnvs, source.GetEnvs()...)
		}
	}

	stsOpts.ExtraEnvs = extraEnvs

	templateFunc := buildMongoDBPodTemplateSpec(*stsOpts)
	return statefulset.New(buildDatabaseStatefulSetConfigurationFunction(mdb, templateFunc, *stsOpts))
}

// buildVaultDatabaseSecretsToInject fully constructs the DatabaseSecretsToInject required to
// convert to annotations to configure vault.
func buildVaultDatabaseSecretsToInject(mdb databaseStatefulSetSource, opts DatabaseStatefulSetOptions) vault.DatabaseSecretsToInject {
	secretsToInject := vault.DatabaseSecretsToInject{Config: opts.VaultConfig}
	if mdb.GetSecurity().ShouldUseX509(opts.CurrentAgentAuthMode) || mdb.GetSecurity().ShouldUseClientCertificates() {
		secretName := mdb.GetSecurity().AgentClientCertificateSecretName(mdb.GetName()).Name
		if opts.CertSecretTypes.IsCertTLSType(secretName) {
			secretName = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)
		}
		secretsToInject.AgentCerts = secretName
	}

	if mdb.GetSecurity().GetInternalClusterAuthenticationMode() == util.X509 {
		secretName := mdb.GetSecurity().InternalClusterAuthSecretName(opts.Name)
		if opts.CertSecretTypes.IsCertTLSType(secretName) {
			secretName = fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)
		}
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

	secretsToInject := buildVaultDatabaseSecretsToInject(mdb, opts)
	volumes, volumeMounts := getVolumesAndVolumeMounts(mdb, opts, secretsToInject.AgentCerts, secretsToInject.InternalClusterAuth)

	allSources := getAllMongoDBVolumeSources(mdb, opts)
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

	annotationFunc := statefulset.NOOP()
	podTemplateAnnotationFunc := podtemplatespec.WithAnnotations(defaultPodAnnotations(opts.CertificateHash))

	if opts.CertSecretTypes.IsCertTLSType(mdb.GetSecurity().MemberCertificateSecretName(opts.Name)) {
		annotationFunc = statefulset.WithAnnotations(defaultPodAnnotations(opts.CertificateHash))
		podTemplateAnnotationFunc = podtemplatespec.NOOP()
	}

	if opts.CertSecretTypes.IsCertTLSType(mdb.GetSecurity().InternalClusterAuthSecretName(opts.Name)) {
		annotationFunc = statefulset.Apply(
			annotationFunc,
			statefulset.WithAnnotations(map[string]string{util.InternalCertAnnotationKey: opts.InternalClusterHash}),
		)
	}

	if vault.IsVaultSecretBackend() {
		podTemplateAnnotationFunc = podtemplatespec.Apply(podTemplateAnnotationFunc, podtemplatespec.WithAnnotations(secretsToInject.DatabaseAnnotations(mdb.GetNamespace())))
	}

	return statefulset.Apply(
		statefulset.WithLabels(ssLabels),
		statefulset.WithName(opts.Name),
		statefulset.WithNamespace(mdb.GetNamespace()),
		statefulset.WithMatchLabels(podLabels),
		statefulset.WithServiceName(opts.ServiceName),
		statefulset.WithReplicas(opts.Replicas),
		statefulset.WithOwnerReference(opts.OwnerReference),
		annotationFunc,
		volumeClaimFuncs,
		statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
			podTemplateAnnotationFunc,
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
		claims, mounts = createClaimsAndMountsMultiModeFunc(opts.PodSpec.Persistence, defaultConfig, opts.Labels)
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
//
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
func getAllMongoDBVolumeSources(mdb databaseStatefulSetSource, databaseOpts DatabaseStatefulSetOptions) []MongoDBVolumeSource {
	caVolume := &caVolumeSource{opts: databaseOpts}
	tlsVolume := &tlsVolumeSource{
		security:     mdb.GetSecurity(),
		databaseOpts: databaseOpts,
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

	volumes := []corev1.Volume{scriptsVolume}
	volumeMounts := []corev1.VolumeMount{databaseScriptsVolumeMount}

	serviceAccountName := getServiceAccountName(opts)

	return podtemplatespec.Apply(
		sharedDatabaseConfiguration(opts),
		podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
		podtemplatespec.WithServiceAccount(serviceAccountName),
		podtemplatespec.WithVolumes(volumes),
		podtemplatespec.WithInitContainerByIndex(0,
			buildDatabaseInitContainer(),
		),
		podtemplatespec.WithContainerByIndex(0,
			container.Apply(
				container.WithName(util.DatabaseContainerName),
				container.WithImage(databaseImageUrl),
				container.WithEnvs(databaseEnvVars(opts)...),
				container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
				container.WithVolumeMounts(volumeMounts),
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
			Name:  util.ENV_VAR_LOG_LEVEL,
			Value: string(podVars.LogLevel),
		},
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
	}

	if opts.PodVars.SSLRequireValidMMSServerCertificates {
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLRequireValidMMSCertificates,
				Value: strconv.FormatBool(opts.PodVars.SSLRequireValidMMSServerCertificates),
			},
		)
	}

	// append any additional env vars specified.
	for _, envVar := range opts.ExtraEnvs {
		vars = append(vars, envVar)
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
