package construct

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	appsv1 "k8s.io/api/apps/v1"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/lifecycle"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	appLabelKey             = "app"
	podAntiAffinityLabelKey = "pod-anti-affinity"
)

// OpsManagerStatefulSetOptions contains all of the different values that are variable between
// StatefulSets. Depending on which StatefulSet is being built, a number of these will be pre-set,
// while the remainder will be configurable via configuration functions which modify this type.
type OpsManagerStatefulSetOptions struct {
	OwnerReference            []metav1.OwnerReference
	HTTPSCertSecretName       string
	AppDBTlsCAConfigMapName   string
	AppDBConnectionStringHash string
	EnvVars                   []corev1.EnvVar
	Version                   string
	Name                      string
	Replicas                  int
	ServiceName               string
	Namespace                 string
	OwnerName                 string
	ServicePort               int
	StatefulSetSpecOverride   *appsv1.StatefulSetSpec

	// backup daemon only
	HeadDbPersistenceConfig *mdbv1.PersistenceConfig
}

func WithConnectionStringHash(hash string) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.AppDBConnectionStringHash = hash
	}
}

// OpsManagerStatefulSet is the base method for building StatefulSet shared by Ops Manager and Backup Daemon.
// Shouldn't be called by end users directly
func OpsManagerStatefulSet(opsManager omv1.MongoDBOpsManager, additionalOpts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
	opts := opsManagerOptions(additionalOpts...)(opsManager)
	omSts := statefulset.New(opsManagerStatefulSetFunc(opts))
	var err error
	if opts.StatefulSetSpecOverride != nil {
		omSts.Spec = merge.StatefulSetSpecs(omSts.Spec, *opts.StatefulSetSpecOverride)
	}

	// the JVM env args must be determined after any potential stateful set override
	// has taken place.
	if err = setJvmArgsEnvVars(opsManager.Spec, util.OpsManagerContainerName, &omSts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return omSts, nil

}

// getSharedOpsManagerOptions returns the options that are shared between both the OpsManager
// and BackupDaemon StatefulSets
func getSharedOpsManagerOptions(opsManager omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
	tlsSecret := ""
	if opsManager.Spec.Security != nil {
		tlsSecret = opsManager.Spec.Security.TLS.SecretRef.Name
	}
	return OpsManagerStatefulSetOptions{
		OwnerReference:          kube.BaseOwnerReference(&opsManager),
		OwnerName:               opsManager.Name,
		HTTPSCertSecretName:     tlsSecret,
		AppDBTlsCAConfigMapName: opsManager.Spec.AppDB.GetCAConfigMapName(),
		EnvVars:                 opsManagerConfigurationToEnvVars(opsManager),
		Version:                 opsManager.Spec.Version,
		Namespace:               opsManager.Namespace,
	}
}

// opsManagerOptions returns a function which returns the OpsManagerStatefulSetOptions to create the OpsManager StatefulSet
func opsManagerOptions(additionalOpts ...func(opts *OpsManagerStatefulSetOptions)) func(opsManager omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
	return func(opsManager omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if opsManager.Spec.StatefulSetConfiguration != nil {
			stsSpec = &opsManager.Spec.StatefulSetConfiguration.Spec
		}

		_, port := opsManager.GetSchemePort()

		opts := getSharedOpsManagerOptions(opsManager)
		opts.ServicePort = port
		opts.ServiceName = opsManager.SvcName()
		opts.Replicas = opsManager.Spec.Replicas
		opts.Name = opsManager.Name
		opts.StatefulSetSpecOverride = stsSpec

		for _, additionalOpt := range additionalOpts {
			additionalOpt(&opts)
		}
		return opts
	}
}

// opsManagerStatefulSetFunc constructs the default Ops Manager StatefulSet modification function
func opsManagerStatefulSetFunc(opts OpsManagerStatefulSetOptions) statefulset.Modification {
	return statefulset.Apply(
		backupAndOpsManagerSharedConfiguration(opts),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				// 5 minutes for Ops Manager just in case (its internal timeout is 20 seconds anyway)
				podtemplatespec.WithTerminationGracePeriodSeconds(300),
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithCommand([]string{"/opt/scripts/docker-entry-point.sh"}),
						container.WithName(util.OpsManagerContainerName),
						container.WithReadinessProbe(opsManagerReadinessProbe(getURIScheme(opts.HTTPSCertSecretName))),
						container.WithLifecycle(buildOpsManagerLifecycle()),
					),
				),
			)),
	)
}

// backupAndOpsManagerSharedConfiguration returns a function which configures all of the shared
// options between the backup and Ops Manager StatefulSet
func backupAndOpsManagerSharedConfiguration(opts OpsManagerStatefulSetOptions) statefulset.Modification {
	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)
	omImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.OpsManagerImageUrl), opts.Version)

	configurePodSpecSecurityContext := podtemplatespec.NOOP()
	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configurePodSpecSecurityContext = podtemplatespec.WithSecurityContext(defaultPodSecurityContext())
		configureContainerSecurityContext = container.WithSecurityContext(defaultSecurityContext())
	}

	pullSecretsConfigurationFunc := podtemplatespec.NOOP()
	if pullSecrets, ok := env.Read(util.ImagePullSecrets); ok {
		pullSecretsConfigurationFunc = podtemplatespec.WithImagePullSecrets(pullSecrets)
	}
	var omVolumeMounts []corev1.VolumeMount

	omScriptsVolume := statefulset.CreateVolumeFromEmptyDir("ops-manager-scripts")
	omScriptsVolumeMount := buildOmScriptsVolumeMount(true)
	omVolumeMounts = append(omVolumeMounts, omScriptsVolumeMount)

	genKeyVolume := statefulset.CreateVolumeFromSecret("gen-key", fmt.Sprintf("%s-gen-key", opts.OwnerName))
	genKeyVolumeMount := corev1.VolumeMount{
		Name:      "gen-key",
		ReadOnly:  true,
		MountPath: util.GenKeyPath,
	}
	omVolumeMounts = append(omVolumeMounts, genKeyVolumeMount)

	omHTTPSVolumeFunc := podtemplatespec.NOOP()
	if opts.HTTPSCertSecretName != "" {
		omHTTPSCertificateVolume := statefulset.CreateVolumeFromSecret("om-https-certificate", opts.HTTPSCertSecretName)
		omHTTPSVolumeFunc = podtemplatespec.WithVolume(omHTTPSCertificateVolume)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      omHTTPSCertificateVolume.Name,
			MountPath: util.MmsPemKeyFileDirInContainer,
		})
	}

	appDbTLSConfigMapVolumeFunc := podtemplatespec.NOOP()
	if opts.AppDBTlsCAConfigMapName != "" {
		appDbTLSVolume := statefulset.CreateVolumeFromConfigMap("appdb-ca-certificate", opts.AppDBTlsCAConfigMapName)
		appDbTLSConfigMapVolumeFunc = podtemplatespec.WithVolume(appDbTLSVolume)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      appDbTLSVolume.Name,
			MountPath: util.MmsCaFileDirInContainer,
		})
	}

	labels := defaultPodLabels(opts.ServiceName, opts.Name)
	return statefulset.Apply(
		statefulset.WithLabels(labels),
		statefulset.WithMatchLabels(labels),
		statefulset.WithName(opts.Name),
		statefulset.WithNamespace(opts.Namespace),
		statefulset.WithOwnerReference(opts.OwnerReference),
		statefulset.WithReplicas(opts.Replicas),
		statefulset.WithServiceName(opts.ServiceName),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				omHTTPSVolumeFunc,
				appDbTLSConfigMapVolumeFunc,
				podtemplatespec.WithVolume(omScriptsVolume),
				podtemplatespec.WithVolume(genKeyVolume),
				podtemplatespec.WithAnnotations(map[string]string{
					"connectionStringHash": opts.AppDBConnectionStringHash,
				}),
				podtemplatespec.WithPodLabels(defaultPodLabels(opts.ServiceName, opts.Name)),
				configurePodSpecSecurityContext,
				pullSecretsConfigurationFunc,
				podtemplatespec.WithServiceAccount(util.OpsManagerServiceAccount),
				podtemplatespec.WithAffinity(opts.Name, podAntiAffinityLabelKey, 100),
				podtemplatespec.WithTopologyKey(util.DefaultAntiAffinityTopologyKey, 0),
				podtemplatespec.WithInitContainerByIndex(0,
					buildOpsManagerAndBackupInitContainer(),
				),
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithResourceRequirements(defaultOpsManagerResourceRequirements()),
						container.WithPorts(buildOpsManagerContainerPorts(opts.HTTPSCertSecretName)),
						container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.OpsManagerPullPolicy))),
						container.WithImage(omImageURL),
						container.WithEnvs(opts.EnvVars...),
						container.WithEnvs(getOpsManagerHTTPSEnvVars(opts.HTTPSCertSecretName)...),
						container.WithCommand([]string{"/opt/scripts/docker-entry-point.sh"}),
						container.WithVolumeMounts(omVolumeMounts),
						configureContainerSecurityContext,
					),
				),
			),
		),
	)
}

func getURIScheme(httpsCertSecretName string) corev1.URIScheme {
	httpsSecretName := httpsCertSecretName
	scheme, _ := omv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		scheme, _ = omv1.SchemePortFromAnnotation("https")
	}
	return scheme
}

// opsManagerReadinessProbe creates the readiness probe.
// Note on 'PeriodSeconds': /monitor/health is a super lightweight method not doing any IO so we can make it more often.
func opsManagerReadinessProbe(scheme corev1.URIScheme) probes.Modification {
	port := 8080
	if scheme == corev1.URISchemeHTTPS {
		port = 8443
	}
	return probes.Apply(
		probes.WithInitialDelaySeconds(60),
		probes.WithTimeoutSeconds(5),
		probes.WithPeriodSeconds(5),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(12),
		probes.WithHandler(corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{Scheme: scheme, Port: intstr.FromInt(port), Path: "/monitor/health"},
		}),
	)
}

// buildOpsManagerAndBackupInitContainer creates the init container which
// copies the entry point script in the OM/Backup container
func buildOpsManagerAndBackupInitContainer() container.Modification {
	version := env.ReadOrDefault(util.InitOpsManagerVersion, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", env.ReadOrPanic(util.InitOpsManagerImageUrl), version)

	managedSecurityContext, _ := env.ReadBool(util.ManagedSecurityContextEnv)

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(defaultSecurityContext())
	}
	return container.Apply(
		container.WithName(util.InitOpsManagerContainerName),
		container.WithImage(initContainerImageURL),
		configureContainerSecurityContext,
		container.WithVolumeMounts([]corev1.VolumeMount{buildOmScriptsVolumeMount(false)}),
	)
}

func buildOmScriptsVolumeMount(readOnly bool) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      "ops-manager-scripts",
		MountPath: "/opt/scripts",
		ReadOnly:  readOnly,
	}
}

func buildOpsManagerLifecycle() lifecycle.Modification {
	return lifecycle.WithPrestopCommand([]string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"})
}

func getOpsManagerHTTPSEnvVars(httpsSecretName string) []corev1.EnvVar {
	if httpsSecretName != "" {
		// Before creating the podTemplate, we need to add the new PemKeyFile
		// configuration if required.
		return []corev1.EnvVar{{
			Name:  omv1.ConvertNameToEnvVarFormat(util.MmsPEMKeyFile),
			Value: util.MmsPemKeyFileDirInContainer + "/server.pem",
		}}
	}
	return []corev1.EnvVar{}
}

func defaultPodLabels(labelKey, antiAffinityKey string) map[string]string {
	return map[string]string{
		appLabelKey:             labelKey,
		ControllerLabelName:     util.OperatorName,
		podAntiAffinityLabelKey: antiAffinityKey,
	}
}

// defaultOpsManagerResourceRequirements returns the default ResourceRequirements
// which are used by OpsManager and the BackupDaemon
func defaultOpsManagerResourceRequirements() corev1.ResourceRequirements {
	defaultMemory, _ := resource.ParseQuantity(util.DefaultMemoryOpsManager)
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: defaultMemory,
		},
		Requests: corev1.ResourceList{},
	}
}

func buildOpsManagerContainerPorts(httpsCertSecretName string) []corev1.ContainerPort {
	return []corev1.ContainerPort{{ContainerPort: int32(getOpsManagerContainerPort(httpsCertSecretName))}}
}

func getOpsManagerContainerPort(httpsSecretName string) int {
	_, port := omv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		_, port = omv1.SchemePortFromAnnotation("https")
	}
	return port
}

// opsManagerConfigurationToEnvVars returns a list of corev1.EnvVar which should be passed
// to the container running Ops Manager
func opsManagerConfigurationToEnvVars(m omv1.MongoDBOpsManager) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for name, value := range m.Spec.Configuration {
		envVars = append(envVars, corev1.EnvVar{
			Name: omv1.ConvertNameToEnvVarFormat(name), Value: value,
		})
	}
	// Configure the AppDB Connection String property from a secret
	envVars = append(envVars, env.FromSecret(omv1.ConvertNameToEnvVarFormat(util.MmsMongoUri), m.AppDBMongoConnectionStringSecretName(), util.AppDbConnectionStringKey))
	return envVars
}
