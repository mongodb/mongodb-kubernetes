package construct

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"

	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"
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

// OpsManagerBuilder is an interface with the methods to construct OpsManager
type OpsManagerBuilder interface {
	GetOwnerRefs() []metav1.OwnerReference
	GetHTTPSCertSecretName() string
	GetAppDBTlsCAConfigMapName() string
	GetAppDBConnectionStringHash() string
	GetEnvVars() []corev1.EnvVar
	GetVersion() string
	GetName() string
	GetService() string
	GetNamespace() string
	GetReplicas() int
	GetOwnerName() string
}

// OpsManagerStatefulSet is the base method for building StatefulSet shared by Ops Manager and Backup Daemon.
// Shouldn't be called by end users directly
// Dev note: it's ok to move the different parts to parameters (pod spec could be an example) as the functionality
// evolves
func OpsManagerStatefulSet(omBuilder OpsManagerBuilder) appsv1.StatefulSet {
	return statefulset.New(opsManagerStatefulSetFunc(omBuilder))
}

// opsManagerStatefulSetFunc constructs the default Ops Manager StatefulSet modification function
func opsManagerStatefulSetFunc(omBuilder OpsManagerBuilder) statefulset.Modification {
	return statefulset.Apply(
		backupAndOpsManagerSharedConfiguration(omBuilder),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				// 5 minutes for Ops Manager just in case (its internal timeout is 20 seconds anyway)
				podtemplatespec.WithTerminationGracePeriodSeconds(300),
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithCommand([]string{"/opt/scripts/docker-entry-point.sh"}),
						container.WithName(util.OpsManagerContainerName),
						container.WithReadinessProbe(opsManagerReadinessProbe(getURIScheme(omBuilder.GetHTTPSCertSecretName()))),
						container.WithLifecycle(buildOpsManagerLifecycle()),
					),
				),
			)),
	)
}

// backupAndOpsManagerSharedConfiguration returns a function which configures all of the shared
// options between the backup and Ops Manager StatefulSet
func backupAndOpsManagerSharedConfiguration(omBuilder OpsManagerBuilder) statefulset.Modification {
	managedSecurityContext, _ := envutil.ReadBool(util.ManagedSecurityContextEnv)
	omImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.OpsManagerImageUrl), omBuilder.GetVersion())

	configurePodSpecSecurityContext := podtemplatespec.NOOP()
	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configurePodSpecSecurityContext = podtemplatespec.WithSecurityContext(defaultPodSecurityContext())
		configureContainerSecurityContext = container.WithSecurityContext(defaultSecurityContext())
	}

	pullSecretsConfigurationFunc := podtemplatespec.NOOP()
	if pullSecrets, ok := envutil.Read(util.ImagePullSecrets); ok {
		pullSecretsConfigurationFunc = podtemplatespec.WithImagePullSecrets(pullSecrets)
	}
	var omVolumeMounts []corev1.VolumeMount

	omScriptsVolume := statefulset.CreateVolumeFromEmptyDir("ops-manager-scripts")
	omScriptsVolumeMount := buildOmScriptsVolumeMount(true)
	omVolumeMounts = append(omVolumeMounts, omScriptsVolumeMount)

	genKeyVolume := statefulset.CreateVolumeFromSecret("gen-key", omBuilder.GetOwnerName()+"-gen-key")
	genKeyVolumeMount := corev1.VolumeMount{
		Name:      "gen-key",
		ReadOnly:  true,
		MountPath: util.GenKeyPath,
	}
	omVolumeMounts = append(omVolumeMounts, genKeyVolumeMount)

	omHTTPSVolumeFunc := podtemplatespec.NOOP()
	if omBuilder.GetHTTPSCertSecretName() != "" {
		omHTTPSCertificateVolume := statefulset.CreateVolumeFromSecret("om-https-certificate", omBuilder.GetHTTPSCertSecretName())
		omHTTPSVolumeFunc = podtemplatespec.WithVolume(omHTTPSCertificateVolume)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      omHTTPSCertificateVolume.Name,
			MountPath: util.MmsPemKeyFileDirInContainer,
		})
	}

	appDbTLSConfigMapVolumeFunc := podtemplatespec.NOOP()
	if omBuilder.GetAppDBTlsCAConfigMapName() != "" {
		appDbTLSVolume := statefulset.CreateVolumeFromConfigMap("appdb-ca-certificate", omBuilder.GetAppDBTlsCAConfigMapName())
		appDbTLSConfigMapVolumeFunc = podtemplatespec.WithVolume(appDbTLSVolume)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      appDbTLSVolume.Name,
			MountPath: util.MmsCaFileDirInContainer,
		})
	}

	labels := defaultPodLabels(omBuilder.GetService(), omBuilder.GetName())
	return statefulset.Apply(
		statefulset.WithLabels(labels),
		statefulset.WithMatchLabels(labels),
		statefulset.WithName(omBuilder.GetName()),
		statefulset.WithNamespace(omBuilder.GetNamespace()),
		statefulset.WithOwnerReference(omBuilder.GetOwnerRefs()),
		statefulset.WithReplicas(omBuilder.GetReplicas()),
		statefulset.WithServiceName(omBuilder.GetService()),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				omHTTPSVolumeFunc,
				appDbTLSConfigMapVolumeFunc,
				podtemplatespec.WithVolume(omScriptsVolume),
				podtemplatespec.WithVolume(genKeyVolume),
				podtemplatespec.WithAnnotations(map[string]string{
					"connectionStringHash": omBuilder.GetAppDBConnectionStringHash(),
				}),
				podtemplatespec.WithPodLabels(defaultPodLabels(omBuilder.GetService(), omBuilder.GetName())),
				configurePodSpecSecurityContext,
				pullSecretsConfigurationFunc,
				podtemplatespec.WithServiceAccount(util.OpsManagerServiceAccount),
				podtemplatespec.WithAffinity(omBuilder.GetName(), podAntiAffinityLabelKey, 100),
				podtemplatespec.WithTopologyKey(util.DefaultAntiAffinityTopologyKey, 0),
				podtemplatespec.WithInitContainerByIndex(0,
					buildOpsManagerAndBackupInitContainer(),
				),
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithResourceRequirements(defaultOpsManagerResourceRequirements()),
						container.WithPorts(buildOpsManagerContainerPorts(omBuilder.GetHTTPSCertSecretName())),
						container.WithImagePullPolicy(corev1.PullPolicy(envutil.ReadOrPanic(util.OpsManagerPullPolicy))),
						container.WithImage(omImageURL),
						container.WithEnvs(omBuilder.GetEnvVars()...),
						container.WithEnvs(getOpsManagerHTTPSEnvVars(omBuilder.GetHTTPSCertSecretName())...),
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
		withTimeoutSeconds(5),
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
	version := envutil.ReadOrDefault(util.InitOpsManagerVersion, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.InitOpsManagerImageUrl), version)

	managedSecurityContext, _ := envutil.ReadBool(util.ManagedSecurityContextEnv)

	configureContainerSecurityContext := container.NOOP()
	if !managedSecurityContext {
		configureContainerSecurityContext = container.WithSecurityContext(defaultSecurityContext())
	}
	return container.Apply(
		container.WithName(util.InitOpsManagerContainerName),
		container.WithImage(initContainerImageURL),
		configureContainerSecurityContext,
		withVolumeMounts([]corev1.VolumeMount{buildOmScriptsVolumeMount(false)}),
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
