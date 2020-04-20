package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"

	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	AppLabelKey = "app"
	// The label that defines the anti affinity rule label. The main rule is to spread entities inside one statefulset
	// (aka replicaset) to different locations, so pods having the same label shouldn't coexist on the node that has
	// the same topology key
	PodAntiAffinityLabelKey = "pod-anti-affinity"

	// ConfigMapVolumeCAName is the name of the volume used to mount CA certs
	ConfigMapVolumeCAName = "secret-ca"

	// CaCertMountPath defines where in the Pod the ca cert will be mounted.
	CaCertMountPath = "/mongodb-automation/certs"

	// AgentCertMountPath defines where in the Pod the ca cert will be mounted.
	AgentCertMountPath = "/mongodb-automation/" + util.AgentSecretName

	// CaCertMMS is the name of the CA file provided for MMS.
	CaCertMMS = "mms-ca.crt"

	// CaCertVolumeName is the name of the volume with the CA Cert
	CaCertName = "ca-cert-volume"

	// AgentLibPath defines the base path for agent configuration files including the automation
	// config file for the headless agent,
	AgentLibPath = "/var/lib/mongodb-automation/"

	// ClusterConfigVolumeName is the name of the volume resource.
	ClusterConfigVolumeName = "cluster-config"

	OneMB = 1048576

	OpsManagerPodMemPercentage = 90
)

// PodVars is a convenience struct to pass environment variables to Pods as needed.
// They are used by the automation agent to connect to Ops/Cloud Manager.
type PodVars struct {
	BaseURL     string
	ProjectID   string
	User        string
	AgentAPIKey string
	LogLevel    mdbv1.LogLevel

	// Related to MMS SSL configuration
	mdbv1.SSLProjectConfig
}

// createBaseDatabaseStatefulSetBuilder is a general function for building the database statefulset
// Reused for building an appdb StatefulSet and a normal mongodb StatefulSet
func createBaseDatabaseStatefulSetBuilder(p StatefulSetHelper, podSpec corev1.PodTemplateSpec) *statefulset.Builder {
	// ssLabels are labels we set to the StatefulSet
	ssLabels := map[string]string{
		AppLabelKey: p.Service,
	}

	stsBuilder := statefulset.NewBuilder().
		SetLabels(ssLabels).
		SetName(p.Name).
		SetNamespace(p.Namespace).
		SetOwnerReference(baseOwnerReference(p.Owner)).
		SetReplicas(util.Int32Ref(int32(p.Replicas))).
		SetPodTemplateSpec(podSpec).
		SetMatchLabels(podSpec.Labels).
		SetServiceName(p.Service)

	if p.Persistent == nil || *p.Persistent {
		claims, mounts := buildPersistentVolumeClaims(p)
		stsBuilder.AddVolumeClaimTemplates(claims).AddVolumeMounts(p.ContainerName, mounts)
	}

	mountVolumes(stsBuilder, p)

	return stsBuilder
}

func defaultPodLabels(stsHelper StatefulSetHelperCommon) map[string]string {
	return map[string]string{
		AppLabelKey:             stsHelper.Service,
		"controller":            util.OmControllerLabel,
		PodAntiAffinityLabelKey: stsHelper.Name,
	}
}


// getDatabasePodTemplate returns the pod template for mongodb pod (MongoDB or AppDB)
func getDatabasePodTemplate(stsHelper StatefulSetHelper,
	annotations map[string]string, serviceAccountName string, container corev1.Container) corev1.PodTemplateSpec {
	podLabels := defaultPodLabels(stsHelper.StatefulSetHelperCommon)
	if annotations == nil {
		annotations = make(map[string]string)
	}
	templateSpec := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers:                    []corev1.Container{container},
			InitContainers:                []corev1.Container{},
			ServiceAccountName:            serviceAccountName,
			TerminationGracePeriodSeconds: util.Int64Ref(util.DefaultPodTerminationPeriodSeconds),
		},
	}

	ensurePodSecurityContext(&templateSpec.Spec)

	templateSpec.ObjectMeta.Labels = podLabels
	templateSpec.Annotations = annotations

	if val, found := util.ReadEnv(util.ImagePullSecrets); found {
		templateSpec.Spec.ImagePullSecrets = append(templateSpec.Spec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: val,
		})
	}
	return configureDefaultAffinityAndResources(templateSpec, stsHelper.PodSpec, stsHelper.Name)
}

// configureDefaultAffinityAndResources updates the pod template created by the Operator based on spec.podSpec specified for the CR
// note, that it doesn't deal with podspec persistence (it's actually for statefulset level, not podtemplate)
func configureDefaultAffinityAndResources(podTemplate corev1.PodTemplateSpec, podSpec *mdbv1.PodSpecWrapper, stsName string) corev1.PodTemplateSpec {
	podTemplate.Spec.Affinity =
		&corev1.Affinity{
			NodeAffinity: podSpec.NodeAffinity,
			PodAffinity:  podSpec.PodAffinity,
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					// Weight thoughts - seems no other affinity rule should be stronger than anti affinity one so putting
					// it to 100
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{PodAntiAffinityLabelKey: stsName}},
						// If PodAntiAffinityTopologyKey config property is empty - then it's ok to use some default (even for standalones)
						TopologyKey: podSpec.GetTopologyKeyOrDefault(),
					},
				}},
			},
		}

	podTemplate.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Limits:   buildLimitsRequirements(podSpec),
		Requests: buildRequestsRequirements(podSpec),
	}
	return podTemplate
}

func newMongoDBContainer(podVars *PodVars) corev1.Container {
	return newDbContainer(util.DatabaseContainerName, util.ReadEnvVarOrPanic(util.AutomationAgentImage), baseEnvFrom(podVars), baseReadinessProbe())
}

func newAppDBContainer(statefulSetName, appdbImageUrl string) corev1.Container {
	return newDbContainer(util.AppDbContainerName, appdbImageUrl, appdbContainerEnv(statefulSetName), baseAppDbReadinessProbe())
}

func newDbContainer(containerName, imageUrl string, envVars []corev1.EnvVar, readinessProbe *corev1.Probe) corev1.Container {
	container := corev1.Container{
		Name:  containerName,
		Image: imageUrl,
		ImagePullPolicy: corev1.PullPolicy(util.ReadEnvVarOrPanic(
			util.AutomationAgentImagePullPolicy)),
		Env:            envVars,
		Ports:          []corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}},
		LivenessProbe:  baseLivenessProbe(),
		ReadinessProbe: readinessProbe,
	}
	return getContainerWithSecurityContext(container)
}

func getContainerWithSecurityContext(container corev1.Container) corev1.Container {
	managedSecurityContext, _ := util.ReadBoolEnv(util.ManagedSecurityContextEnv)
	if !managedSecurityContext {
		container.SecurityContext = &corev1.SecurityContext{
			RunAsUser:    util.Int64Ref(util.RunAsUser),
			RunAsNonRoot: util.BooleanRef(true),
		}
	}
	return container
}

// buildStatefulSet builds the StatefulSet of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources.
// This is a convenience method to pass all attributes inside a "parameters" object which is easier to
// build in client code and avoid passing too many different parameters to `buildStatefulSet`.
func buildStatefulSet(p StatefulSetHelper) (appsv1.StatefulSet, error) {
	annotations := map[string]string{
		// this annotation is necessary in order to trigger a pod restart
		// if the certificate secret is out of date. This happens if
		// existing certificates have been replaced/rotated/renewed.
		"certHash": p.CertificateHash,
	}
	// podLabels are labels we set to StatefulSet Selector and Template.Meta
	template := getDatabasePodTemplate(p, annotations, util.MongoDBServiceAccount,
		newMongoDBContainer(p.PodVars))

	return createBaseDatabaseStatefulSetBuilder(p, template).Build()
}

// TODO: REMOVE ONCE INIT APPDB IS IMPLEMENTED
// prepareOmAppdbImageUrl builds the full image url for OM/AppDB images
// It optionally appends the suffix "-operator<operatorVersion" to distinguish the images built for different Operator
// releases. It's used in production and Evergreen runs (where the new images are built on each Evergreen run)
// It's not used for local development where the Operator version is just not specified.
// So far it seems that no other logic depends on the Operator version so we can afford this - we can complicate things
// if requirements change
func prepareOmAppdbImageUrl(imageUrl, version string) string {
	// how does this work when the -operator is appended?
	fullImageUrl := fmt.Sprintf("%s:%s", imageUrl, version)
	if util.OperatorVersion != "" {
		fullImageUrl = fmt.Sprintf("%s-operator%s", fullImageUrl, util.OperatorVersion)
	}
	return fullImageUrl
}

// buildAppDbStatefulSet builds the StatefulSet for AppDB.
// It's mostly the same as the normal MongoDB one but has slightly different container and an additional mounting volume
func buildAppDbStatefulSet(p StatefulSetHelper) (appsv1.StatefulSet, error) {
	appdbImageUrl := prepareOmAppdbImageUrl(util.ReadEnvVarOrPanic(util.AppDBImageUrl), p.Version)
	template := getDatabasePodTemplate(p, nil, util.AppDBServiceAccount,
		newAppDBContainer(p.Name, appdbImageUrl))

	stsBuilder := createBaseDatabaseStatefulSetBuilder(p, template)

	stsBuilder.AddVolumeAndMount(
		statefulset.VolumeMountData{
			Name:      ClusterConfigVolumeName,
			MountPath: AgentLibPath,
			ReadOnly:  true,
			Volume:    statefulset.CreateVolumeFromConfigMap(ClusterConfigVolumeName, p.Name+"-config"),
		},
		p.ContainerName,
	)

	return stsBuilder.Build()
}

// createBaseOpsManagerStatefulSetBuilder is the base method for building StatefulSet shared by Ops Manager and Backup Daemon.
// Shouldn't be called by end users directly
// Dev note: it's ok to move the different parts to parameters (pod spec could be an example) as the functionality
// evolves
func createBaseOpsManagerStatefulSetBuilder(p OpsManagerStatefulSetHelper, containerSpec corev1.Container) (*statefulset.Builder, error) {
	labels := defaultPodLabels(p.StatefulSetHelperCommon)

	template := opsManagerPodTemplate(labels, p, containerSpec)

	jvmParamsEnvVars, err := buildJvmParamsEnvVars(p.Spec, template)
	if err != nil {
		return nil, err
	}

	// pass Xmx java parameter to container
	for _, envVar := range jvmParamsEnvVars {
		template.Spec.Containers[0].Env = append(template.Spec.Containers[0].Env, envVar)
	}

	stsBuilder := statefulset.NewBuilder().
		SetLabels(labels).
		SetName(p.Name).
		SetNamespace(p.Namespace).
		SetOwnerReference(baseOwnerReference(p.Owner)).
		SetReplicas(util.Int32Ref(int32(p.Replicas))).
		SetPodTemplateSpec(template).
		SetMatchLabels(labels).
		SetServiceName(p.Service)

	mountData := statefulset.VolumeMountData{
		Name:      "gen-key",
		Volume:    statefulset.CreateVolumeFromSecret("gen-key", p.Owner.GetName()+"-gen-key"),
		ReadOnly:  true,
		MountPath: util.GenKeyPath,
	}
	stsBuilder.AddVolumeAndMount(mountData, p.ContainerName)

	if p.HTTPSCertSecretName != "" {
		mountCert := statefulset.VolumeMountData{
			Name:      "om-https-certificate",
			Volume:    statefulset.CreateVolumeFromSecret("om-https-certificate", p.HTTPSCertSecretName),
			MountPath: util.MmsPemKeyFileDirInContainer,
		}
		stsBuilder.AddVolumeAndMount(mountCert, p.ContainerName)
	}

	if p.AppDBTlsCAConfigMapName != "" {
		mountCaCert := statefulset.VolumeMountData{
			Name:      "appdb-ca-certificate",
			Volume:    statefulset.CreateVolumeFromConfigMap("appdb-ca-certificate", p.AppDBTlsCAConfigMapName),
			MountPath: util.MmsCaFileDirInContainer,
		}
		stsBuilder.AddVolumeAndMount(mountCaCert, p.ContainerName)
	}

	stsBuilder.AddVolume(
		corev1.Volume{
			Name: "ops-manager-scripts",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)
	stsBuilder.AddVolumeMount(
		"mongodb-enterprise-init-ops-manager",
		corev1.VolumeMount{
			Name:      "ops-manager-scripts",
			MountPath: "/opt/scripts",
			ReadOnly:  false,
		},
	)
	stsBuilder.AddVolumeMount(
		p.ContainerName,
		corev1.VolumeMount{
			Name:      "ops-manager-scripts",
			MountPath: "/opt/scripts",
			ReadOnly:  true,
		},
	)

	return stsBuilder, nil
}

// buildOpsManagerStatefulSet builds the StatefulSet for Ops Manager
func buildOpsManagerStatefulSet(p OpsManagerStatefulSetHelper) (appsv1.StatefulSet, error) {
	container := buildOpsManagerContainer(p)
	stsBuilder, err := createBaseOpsManagerStatefulSetBuilder(p, container)
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	return stsBuilder.Build()
}

func buildBaseContainerForOpsManagerAndBackup(p OpsManagerStatefulSetHelper) corev1.Container {
	httpsSecretName := p.HTTPSCertSecretName
	_, port := mdbv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		_, port = mdbv1.SchemePortFromAnnotation("https")

		// Before creating the podTemplate, we need to add the new PemKeyFile
		// configuration if required.
		p.EnvVars = append(p.EnvVars, corev1.EnvVar{
			Name:  mdbv1.ConvertNameToEnvVarFormat(util.MmsPEMKeyFile),
			Value: util.MmsPemKeyFileDirInContainer + "/server.pem",
		})
	}

	omImageUrl := fmt.Sprintf("%s:%s", util.ReadEnvVarOrPanic(util.OpsManagerImageUrl), p.Version)
	container := corev1.Container{
		Image:           omImageUrl,
		ImagePullPolicy: corev1.PullPolicy(util.ReadEnvVarOrPanic(util.OpsManagerPullPolicy)),
		Env:             p.EnvVars,
		Ports:           []corev1.ContainerPort{{ContainerPort: int32(port)}},
	}
	return container
}

func buildOpsManagerContainer(p OpsManagerStatefulSetHelper) corev1.Container {
	httpsSecretName := p.HTTPSCertSecretName
	scheme, _ := mdbv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		scheme, _ = mdbv1.SchemePortFromAnnotation("https")
	}

	container := buildBaseContainerForOpsManagerAndBackup(p)
	container.Name = p.ContainerName
	container.ReadinessProbe = opsManagerReadinessProbe(scheme)
	container.Lifecycle = &corev1.Lifecycle{
		PreStop: &corev1.Handler{
			Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"},
			},
		},
	}
	container.Command = []string{"/opt/scripts/docker-entry-point.sh"}
	return container
}

func buildBackupDaemonContainer(p BackupStatefulSetHelper) corev1.Container {
	container := buildBaseContainerForOpsManagerAndBackup(p.OpsManagerStatefulSetHelper)
	container.Name = p.ContainerName
	container.Lifecycle = &corev1.Lifecycle{
		PreStop: &corev1.Handler{
			Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"},
			},
		},
	}
	container.Command = []string{"/opt/scripts/docker-entry-point.sh"}
	return container
}

// buildBackupDaemonStatefulSet builds the StatefulSet for backup daemon. It shares most of the configuration with
// OpsManager StatefulSet adding something on top of it
func buildBackupDaemonStatefulSet(p BackupStatefulSetHelper) (appsv1.StatefulSet, error) {
	p.EnvVars = append(p.EnvVars, corev1.EnvVar{
		// For the OM Docker image to run as Backup Daemon, the BACKUP_DAEMON env variable
		// needs to be passed with any value.
		Name:  util.ENV_BACKUP_DAEMON,
		Value: "backup",
	})
	container := buildBackupDaemonContainer(p)

	stsBuilder, err := createBaseOpsManagerStatefulSetBuilder(p.OpsManagerStatefulSetHelper, container)
	if err != nil {
		return appsv1.StatefulSet{}, err
	}

	// Mount head db
	defaultConfig := mdbv1.PersistenceConfig{Storage: util.DefaultHeadDbStorageSize}

	stsBuilder.AddVolumeClaimTemplates(
		[]corev1.PersistentVolumeClaim{pvc(util.PvcNameHeadDb, p.HeadDbPersistenceConfig, defaultConfig)},
	).
		AddVolumeMounts(
			util.BackupDaemonContainerName,
			[]corev1.VolumeMount{statefulset.CreateVolumeMount(util.PvcNameHeadDb, util.PvcMountPathHeadDb, "")},
		)

	return stsBuilder.Build()
}

func buildJvmParamsEnvVars(m mdbv1.MongoDBOpsManagerSpec, template corev1.PodTemplateSpec) ([]corev1.EnvVar, error) {
	mmsJvmEnvVar := corev1.EnvVar{Name: util.MmsJvmParamEnvVar}
	backupJvmEnvVar := corev1.EnvVar{Name: util.BackupDaemonJvmParamEnvVar}

	// calculate xmx from container's memory limit
	memLimits := template.Spec.Containers[0].Resources.Limits.Memory()
	maxPodMem, err := getPercentOfQuantityAsInt(*memLimits, OpsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, fmt.Errorf("error calculating xmx from pod mem: %e", err)
	}

	// calculate xms from container's memory request if it is set, otherwise xms=xmx
	memRequests := template.Spec.Containers[0].Resources.Requests.Memory()
	minPodMem, err := getPercentOfQuantityAsInt(*memRequests, OpsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, fmt.Errorf("error calculating xms from pod mem: %e", err)
	}

	// if only one of mem limits/requests is set, use that value for both xmx & xms
	if minPodMem == 0 {
		minPodMem = maxPodMem
	}
	if maxPodMem == 0 {
		maxPodMem = minPodMem
	}

	memParams := fmt.Sprintf("-Xmx%dm -Xms%dm", maxPodMem, minPodMem)
	mmsJvmEnvVar.Value = buildJvmEnvVar(m.JVMParams, memParams)
	backupJvmEnvVar.Value = buildJvmEnvVar(m.Backup.JVMParams, memParams)

	return []corev1.EnvVar{mmsJvmEnvVar, backupJvmEnvVar}, nil
}

func getPercentOfQuantityAsInt(q resource.Quantity, percent int) (int, error) {
	quantityAsInt, canConvert := q.AsInt64()
	if !canConvert {
		// the container's mem can't be converted to int64, use default of 5G
		podMem, err := resource.ParseQuantity(util.DefaultMemoryOpsManager)
		quantityAsInt, canConvert = podMem.AsInt64()
		if err != nil {
			return 0, err
		}
		if !canConvert {
			return 0, fmt.Errorf("cannot convert %s to int64", podMem.String())
		}
	}
	percentage := float64(percent) / 100.0

	return int(float64(quantityAsInt)*percentage) / OneMB, nil
}

func buildJvmEnvVar(customParams []string, containerMemParams string) string {
	jvmParams := strings.Join(customParams, " ")

	// if both mem limits and mem requests are unset/have value 0, we don't want to override om's default JVM xmx/xms params
	if strings.Contains(containerMemParams, "-Xmx0m") {
		return jvmParams
	}

	if strings.Contains(jvmParams, "Xmx") {
		return jvmParams
	}

	if jvmParams != "" {
		jvmParams += " "
	}

	return jvmParams + containerMemParams
}

// mountVolumes will add VolumeMounts to the `stsBuilder` object.
// Make sure you keep this updated with `kubehelper.needToPublishStateFirst` as it declares
// in which order to make changes to StatefulSet and Ops Manager automationConfig
func mountVolumes(stsBuilder *statefulset.Builder, helper StatefulSetHelper) *statefulset.Builder {
	if helper.Security != nil {
		// TLS configuration is active for this resource.
		if helper.Security.TLSConfig.Enabled || helper.Security.TLSConfig.SecretRef.Name != "" {
			tlsConfig := helper.Security.TLSConfig

			var secretName string
			if helper.Security.TLSConfig.SecretRef.Name != "" {
				// From this location, the certificates will be used inplace
				secretName = helper.Security.TLSConfig.SecretRef.Name
			} else {
				// In this location the certificates will be linked -s into server.pem
				secretName = fmt.Sprintf("%s-cert", helper.Name)
			}

			stsBuilder.AddVolumeAndMount(
				statefulset.VolumeMountData{
					MountPath: util.SecretVolumeMountPath + "/certs",
					Name:      util.SecretVolumeName,
					ReadOnly:  true,
					Volume:    statefulset.CreateVolumeFromSecret(util.SecretVolumeName, secretName),
				},
				helper.ContainerName,
			)

			if tlsConfig.CA != "" {
				stsBuilder.AddVolumeAndMount(
					statefulset.VolumeMountData{
						MountPath: util.ConfigMapVolumeCAMountPath,
						Name:      ConfigMapVolumeCAName,
						ReadOnly:  true,
						Volume:    statefulset.CreateVolumeFromConfigMap(ConfigMapVolumeCAName, tlsConfig.CA),
					},
					helper.ContainerName,
				)
			}
		}
	}

	if helper.PodVars != nil && helper.PodVars.SSLMMSCAConfigMap != "" {
		stsBuilder.AddVolumeAndMount(
			statefulset.VolumeMountData{
				MountPath: CaCertMountPath,
				Name:      CaCertName,
				ReadOnly:  true,
				Volume:    statefulset.CreateVolumeFromConfigMap(CaCertName, helper.PodVars.SSLMMSCAConfigMap),
			},
			helper.ContainerName,
		)
	}

	if helper.Security != nil {
		if helper.Security.Authentication.GetAgentMechanism() == util.X509 {
			stsBuilder.AddVolumeAndMount(
				statefulset.VolumeMountData{
					MountPath: AgentCertMountPath,
					Name:      util.AgentSecretName,
					ReadOnly:  true,
					Volume:    statefulset.CreateVolumeFromSecret(util.AgentSecretName, util.AgentSecretName),
				},
				helper.ContainerName,
			)
		}

		// add volume for x509 cert used in internal cluster authentication
		if helper.Security.Authentication.InternalCluster == util.X509 {
			stsBuilder.AddVolumeAndMount(
				statefulset.VolumeMountData{
					MountPath: util.InternalClusterAuthMountPath,
					Name:      util.ClusterFileName,
					ReadOnly:  true,
					Volume:    statefulset.CreateVolumeFromSecret(util.ClusterFileName, toInternalClusterAuthName(helper.Name)),
				},
				helper.ContainerName,
			)
		}
	}
	return stsBuilder
}

func buildPersistentVolumeClaims(p StatefulSetHelper) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
	var claims []corev1.PersistentVolumeClaim
	var mounts []corev1.VolumeMount

	// if persistence not set or if single one is set
	if p.PodSpec.Persistence == nil ||
		(p.PodSpec.Persistence.SingleConfig == nil && p.PodSpec.Persistence.MultipleConfig == nil) ||
		p.PodSpec.Persistence.SingleConfig != nil {
		var config *mdbv1.PersistenceConfig
		if p.PodSpec.Persistence != nil && p.PodSpec.Persistence.SingleConfig != nil {
			config = p.PodSpec.Persistence.SingleConfig
		}
		// Single claim, multiple mounts using this claim. Note, that we use "subpaths" in the volume to mount to different
		// physical folders
		claims, mounts = createClaimsAndMountsSingleMode(config, p)
	} else if p.PodSpec.Persistence.MultipleConfig != nil {
		defaultConfig := *p.PodSpec.Default.Persistence.MultipleConfig

		// Multiple claims, multiple mounts. No subpaths are used and everything is mounted to the root of directory
		claims, mounts = createClaimsAndMontsMultiMode(p.PodSpec.Persistence, defaultConfig)
	}
	return claims, mounts
}

// buildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable.
// This function will update a Service object if passed, or return a new one if passed nil, this is to be able to update
// Services and to not change any attribute they might already have that needs to be maintained.
//
func buildService(namespacedName types.NamespacedName, owner Updatable, label string, port int32, mongoServiceDefinition mdbv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
	svcBuilder := service.Builder().
		SetNamespace(namespacedName.Namespace).
		SetName(namespacedName.Name).
		SetPort(port).
		SetOwnerReferences(baseOwnerReference(owner)).
		SetLabels(map[string]string{
			AppLabelKey: label,
		}).SetSelector(map[string]string{
		AppLabelKey: label,
	}).SetServiceType(mongoServiceDefinition.Type)

	serviceType := mongoServiceDefinition.Type
	if serviceType == corev1.ServiceTypeNodePort || serviceType == corev1.ServiceTypeLoadBalancer {
		svcBuilder.SetClusterIP("").SetNodePort(mongoServiceDefinition.Port)
	}

	if serviceType == corev1.ServiceTypeClusterIP {
		svcBuilder.SetPublishNotReadyAddresses(true).SetClusterIP("None").SetPortName("mongodb")
	}

	if mongoServiceDefinition.Annotations != nil {
		svcBuilder.SetAnnotations(mongoServiceDefinition.Annotations)
	}

	if mongoServiceDefinition.LoadBalancerIP != "" {
		svcBuilder.SetLoadBalancerIP(mongoServiceDefinition.LoadBalancerIP)
	}

	if mongoServiceDefinition.ExternalTrafficPolicy != "" {
		svcBuilder.SetExternalTrafficPolicy(mongoServiceDefinition.ExternalTrafficPolicy)
	}

	return svcBuilder.Build()
}

func baseOwnerReference(owner Updatable) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, schema.GroupVersionKind{
			Group:   mdbv1.SchemeGroupVersion.Group,
			Version: mdbv1.SchemeGroupVersion.Version,
			Kind:    owner.GetKind(),
		}),
	}
}

// ensurePodSecurityContext adds the 'SecurityContext' to the pod spec if it's necessary (Openshift doesn't need this
// as it manages the security by itself)
func ensurePodSecurityContext(spec *corev1.PodSpec) {
	managedSecurityContext, _ := util.ReadBoolEnv(util.ManagedSecurityContextEnv)
	if !managedSecurityContext {
		spec.SecurityContext = &corev1.PodSecurityContext{
			FSGroup: util.Int64Ref(util.FsGroup),
		}
	}
}

func appdbContainerEnv(statefulSetName string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:      util.ENV_POD_NAMESPACE,
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			Name: util.ENV_AUTOMATION_CONFIG_MAP,
			// not critical but would be nice to reuse `AppDB.AutomationConfigSecretName`
			Value: statefulSetName + "-config",
		},
		{
			Name:  util.ENV_HEADLESS_AGENT,
			Value: "true",
		},
	}
}

// opsManagerPodTemplate builds the pod template spec used by both Backup and OM statefulsets
// In the end it applies the podSpec (and probably podSpec.podTemplate) as the MongoDB and AppDB do.
func opsManagerPodTemplate(labels map[string]string, stsHelper OpsManagerStatefulSetHelper, containerSpec corev1.Container) corev1.PodTemplateSpec {
	version := util.ReadEnvVarOrDefault(util.InitOpsManagerVersion, "latest")
	imageUrl := fmt.Sprintf("%s:%s", util.ReadEnvVarOrPanic(util.InitOpsManagerImageUrl), version)
	templateSpec := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name:  "mongodb-enterprise-init-ops-manager",
				Image: imageUrl,
				// FIME: temporary to fix evg tests
				ImagePullPolicy: "Always",
			}},
			Containers: []corev1.Container{getContainerWithSecurityContext(containerSpec)},
			// After discussion with John Morales seems 30 min should be ok
			// Note, that the OM (as of current version 4.2.8) has internal timeout of 20 seconds for 'stop_mms' and
			// backup daemon doesn't have graceful timeout for 'stop_backup_daemon' at all
			TerminationGracePeriodSeconds: util.Int64Ref(1800),
		},
	}

	ensurePodSecurityContext(&templateSpec.Spec)
	if val, found := util.ReadEnv(util.ImagePullSecrets); found {
		templateSpec.Spec.ImagePullSecrets = append(templateSpec.Spec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: val,
		})
	}
	return configureDefaultAffinityAndResources(templateSpec, stsHelper.PodSpec, stsHelper.Name)
}

func baseLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.LivenessProbe}},
		},
		InitialDelaySeconds: 60,
		TimeoutSeconds:      30,
		PeriodSeconds:       30,
		SuccessThreshold:    1,
		FailureThreshold:    6,
	}
}

// opsManagerReadinessProbe creates the readiness probe.
// Note on 'PeriodSeconds': /monitor/health is a super lightweight method not doing any IO so we can make it more often.
func opsManagerReadinessProbe(scheme corev1.URIScheme) *corev1.Probe {
	port := 8080
	if scheme == corev1.URISchemeHTTPS {
		port = 8443
	}
	return &corev1.Probe{
		Handler: corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{Scheme: scheme, Port: intstr.FromInt(port), Path: "/monitor/health"},
		},
		InitialDelaySeconds: 60,
		TimeoutSeconds:      5,
		PeriodSeconds:       5,
		SuccessThreshold:    1,
		FailureThreshold:    12, // So the probe will fail after 1 minute of Ops Manager being non-responsive
	}
}

func baseReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.ReadinessProbe}},
		},
		// Setting the failure threshold to quite big value as the agent may spend some time to reach the goal
		FailureThreshold: 240,
		// The agent may be not on time to write the status file right after the container is created - we need to wait
		// for some time
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
	}
}
func baseAppDbReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.ReadinessProbe}},
		},
		// Need to set to 1 to make readiness "interactive" and to indicate whether the agent has reached the goal or not
		FailureThreshold: 1,
		// The agent may be not on time to write the status file right after the container is created - we need to wait
		// for some time (todo check this)
		InitialDelaySeconds: 5,
		// We need more frequent check to provide faster response to the Operator
		PeriodSeconds: 5,
	}
}

func baseEnvFrom(podVars *PodVars) []corev1.EnvVar {
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
		{
			Name:  util.ENV_VAR_AGENT_API_KEY,
			Value: podVars.AgentAPIKey,
		},
		{
			Name:  util.ENV_VAR_LOG_LEVEL,
			Value: string(podVars.LogLevel),
		},
	}

	if podVars.SSLRequireValidMMSServerCertificates {
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLRequireValidMMSCertificates,
				Value: strconv.FormatBool(podVars.SSLRequireValidMMSServerCertificates),
			},
		)
	}

	if podVars.SSLMMSCAConfigMap != "" {
		// A custom CA has been provided, point the trusted CA to the location of custom CAs
		trustedCACertLocation := path.Join(CaCertMountPath, CaCertMMS)
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLTrustedMMSServerCertificate,
				Value: trustedCACertLocation,
			},
		)
	}

	return vars
}

func createClaimsAndMontsMultiMode(persistence *mdbv1.Persistence, defaultConfig mdbv1.MultiplePersistenceConfig) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
	claims := []corev1.PersistentVolumeClaim{
		pvc(util.PvcNameData, persistence.MultipleConfig.Data, *defaultConfig.Data),
		pvc(util.PvcNameJournal, persistence.MultipleConfig.Journal, *defaultConfig.Journal),
		pvc(util.PvcNameLogs, persistence.MultipleConfig.Logs, *defaultConfig.Logs),
	}
	mounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData, ""),
		statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal, ""),
		statefulset.CreateVolumeMount(util.PvcNameLogs, util.PvcMountPathLogs, ""),
	}
	return claims, mounts
}

func createClaimsAndMountsSingleMode(config *mdbv1.PersistenceConfig, p StatefulSetHelper) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
	claims := []corev1.PersistentVolumeClaim{pvc(util.PvcNameData, config, *p.PodSpec.Default.Persistence.SingleConfig)}
	mounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData, util.PvcNameData),
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathJournal, util.PvcNameJournal),
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathLogs, util.PvcNameLogs),
	}
	return claims, mounts
}

// pvc convenience function to build a PersistentVolumeClaim. It accepts two config parameters - the one specified by
// the customers and the default one configured by the Operator. Putting the default one to the signature ensures the
// calling code doesn't forget to think about default values in case the user hasn't provided values.
func pvc(name string, config *mdbv1.PersistenceConfig, defaultConfig mdbv1.PersistenceConfig) corev1.PersistentVolumeClaim {
	claim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: buildStorageRequirements(config, defaultConfig),
			},
		},
	}
	if config != nil {
		claim.Spec.Selector = config.LabelSelector
		claim.Spec.StorageClassName = config.StorageClass
	}
	return claim
}
