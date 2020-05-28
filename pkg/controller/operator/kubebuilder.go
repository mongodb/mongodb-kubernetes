package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"path"
	"strconv"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/podtemplatespec"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"

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
	// PodAntiAffinityLabelKey defines the anti affinity rule label. The main rule is to spread entities inside one statefulset
	// (aka replicaset) to different locations, so pods having the same label shouldn't coexist on the node that has
	// the same topology key
	PodAntiAffinityLabelKey = "pod-anti-affinity"

	// ConfigMapVolumeCAName is the name of the volume used to mount CA certs
	ConfigMapVolumeCAName = "secret-ca"

	// CaCertMountPath defines where in the Pod the ca cert will be mounted.
	CaCertMountPath = "/mongodb-automation/certs"

	// AgentCertMountPath defines where in the Pod the ca cert will be mounted.
	AgentCertMountPath = "/mongodb-automation/" + util.AgentSecretName

	// CaCertName is the name of the volume with the CA Cert
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

func defaultPodAnnotations(certHash string) map[string]string {
	return map[string]string{
		// this annotation is necessary in order to trigger a pod restart
		// if the certificate secret is out of date. This happens if
		// existing certificates have been replaced/rotated/renewed.
		"certHash": certHash,
	}
}

// sharedDatabaseConfigurationConfiguration is a function which applies all the shared configuration
// between the appDb and MongoDB resources
func sharedDatabaseConfigurationConfiguration(stsHelper StatefulSetHelper) func(podTemplateSpec *corev1.PodTemplateSpec) {
	managedSecurityContext, _ := envutil.ReadBool(util.ManagedSecurityContextEnv)
	modificationFunctions := []func(podTemplateSpec *corev1.PodTemplateSpec){
		podtemplatespec.WithPodLabels(defaultPodLabels(stsHelper.StatefulSetHelperCommon)),
		podtemplatespec.WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds),
		podtemplatespec.WithSecurityContext(managedSecurityContext),
		podtemplatespec.WithImagePullSecrets(),
		podtemplatespec.WithAffinity(stsHelper.Name, PodAntiAffinityLabelKey),
		podtemplatespec.WithNodeAffinity(stsHelper.PodSpec.NodeAffinity),
		podtemplatespec.WithPodAffinity(stsHelper.PodSpec.PodAffinity),
		podtemplatespec.WithTopologyKey(stsHelper.PodSpec.GetTopologyKeyOrDefault()),
		podtemplatespec.WithContainers(podtemplatespec.BuildContainer(
			// database uses podSpec as normal
			// TODO: remove in favour of spec.statefulSet
			podtemplatespec.WithContainerResources(buildRequirementsFromPodSpec(*stsHelper.PodSpec)),
			podtemplatespec.WithContainerPorts([]corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}}),
			podtemplatespec.WithContainerPullPolicy(corev1.PullPolicy(envutil.ReadOrPanic(util.AutomationAgentImagePullPolicy))),
			podtemplatespec.WithContainerLivenessProbe(baseLivenessProbe()),
			podtemplatespec.WithContainerSecurityContext(managedSecurityContext),
		)),
	}
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		for _, opt := range modificationFunctions {
			opt(podTemplateSpec)
		}
	}
}

// buildMongoDBPodTemplateSpec constructs the podTemplateSpec for the MongoDB resource
func buildMongoDBPodTemplateSpec(stsHelper StatefulSetHelper) corev1.PodTemplateSpec {
	return podtemplatespec.Build(
		sharedDatabaseConfigurationConfiguration(stsHelper),
		podtemplatespec.WithAnnotations(defaultPodAnnotations(stsHelper.CertificateHash)),
		podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
		podtemplatespec.EditContainer(0,
			podtemplatespec.WithContainerName(util.DatabaseContainerName),
			podtemplatespec.WithContainerImage(envutil.ReadOrPanic(util.AutomationAgentImage)),
			podtemplatespec.WithContainerEnvVars(databaseEnvVars(stsHelper.PodVars)...),
			podtemplatespec.WithContainerReadinessProbe(buildDatabaseReadinessProbe()),
		),
	)
}

// buildAppDBPodTemplateSpec constructs the appDb podTemplateSpec
func buildAppDBPodTemplateSpec(stsHelper StatefulSetHelper) corev1.PodTemplateSpec {
	// AppDB only uses the automation agent in headless mode, let's use the latest version
	appdbImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.AppDBImageUrl),
		envutil.ReadOrDefault(util.AppDBAutomationAgentVersion, "latest"))
	return podtemplatespec.Build(
		sharedDatabaseConfigurationConfiguration(stsHelper),
		podtemplatespec.WithAnnotations(map[string]string{}),
		podtemplatespec.WithServiceAccount(util.AppDBServiceAccount),
		podtemplatespec.WithInitContainers(
			buildAppdbInitContainer(),
		),
		podtemplatespec.EditContainer(0,
			podtemplatespec.WithContainerName(util.AppDbContainerName),
			podtemplatespec.WithContainerImage(appdbImageURL),
			podtemplatespec.WithContainerEnvVars(appdbContainerEnv(stsHelper.Name)...),
			podtemplatespec.WithContainerReadinessProbe(buildAppDbReadinessProbe()),
			podtemplatespec.WithContainerLivenessProbe(baseAppDbLivenessProbe()),
			podtemplatespec.WithContainerCommand([]string{"/opt/scripts/agent-launcher.sh"}),
		),
	)
}

// buildAppdbInitContainer creates the init container which
// copies the entry point script in the AppDB container
func buildAppdbInitContainer() corev1.Container {
	version := envutil.ReadOrDefault(util.InitAppdbVersion, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.InitAppdbImageUrl), version)
	return podtemplatespec.BuildContainer(
		podtemplatespec.WithContainerName(util.InitAppDbContainerName),
		podtemplatespec.WithContainerImage(initContainerImageURL),
	)
}

// buildOpsManagerPodTemplateSpec constructs the default Ops Manager podTemplateSpec
func buildOpsManagerPodTemplateSpec(stsHelper OpsManagerStatefulSetHelper) (corev1.PodTemplateSpec, error) {
	podTemplateSpec := podtemplatespec.Build(
		backupAndOpsManagerConfiguration(stsHelper),
		// 5 minutes for Ops Manager just in case (its internal timeout is 20 seconds anyway)
		podtemplatespec.WithTerminationGracePeriodSeconds(300),
		podtemplatespec.EditContainer(0,
			podtemplatespec.WithContainerName(util.OpsManagerContainerName),
			podtemplatespec.WithContainerReadinessProbe(opsManagerReadinessProbe(getURIScheme(stsHelper.HTTPSCertSecretName))),
			podtemplatespec.WithContainerLifeCycle(buildOpsManagerLifecycle()),
		),
	)
	return podTemplateSpec, nil
}

// buildBackupDaemonPodTemplateSpec constructs the Backup Daemon podTemplateSpec
func buildBackupDaemonPodTemplateSpec(stsHelper BackupStatefulSetHelper) (corev1.PodTemplateSpec, error) {
	podTemplateSpec := podtemplatespec.Build(
		backupAndOpsManagerConfiguration(stsHelper.OpsManagerStatefulSetHelper),
		// 70 minutes for Backup Damon (internal timeout is 65 minutes, see CLOUDP-61849)
		podtemplatespec.WithTerminationGracePeriodSeconds(4200),
		podtemplatespec.EditContainer(0,
			podtemplatespec.WithContainerName(util.BackupDaemonContainerName),
			podtemplatespec.WithContainerEnvVars(backupDaemonEnvVars()...),
			podtemplatespec.WithContainerLifeCycle(buildBackupDaemonLifecycle()),
		),
	)
	return podTemplateSpec, nil
}

// backupAndOpsManagerConfiguration returns a function which configures all of the shared
// options between the backup and Ops Manager podTemplateSpecs
func backupAndOpsManagerConfiguration(stsHelper OpsManagerStatefulSetHelper) func(podTemplateSpec *corev1.PodTemplateSpec) {
	omImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.OpsManagerImageUrl), stsHelper.Version)
	managedSecurityContext, _ := envutil.ReadBool(util.ManagedSecurityContextEnv)
	modificationFunctions := []func(podTemplateSpec *corev1.PodTemplateSpec){
		podtemplatespec.WithAnnotations(map[string]string{
			"connectionStringHash": stsHelper.AppDBConnectionStringHash,
		}),
		podtemplatespec.WithPodLabels(defaultPodLabels(stsHelper.StatefulSetHelperCommon)),
		podtemplatespec.WithSecurityContext(managedSecurityContext),
		podtemplatespec.WithServiceAccount(util.OpsManagerServiceAccount),
		podtemplatespec.WithImagePullSecrets(),
		podtemplatespec.WithAffinity(stsHelper.Name, PodAntiAffinityLabelKey),
		podtemplatespec.WithTopologyKey(util.DefaultAntiAffinityTopologyKey),
		podtemplatespec.WithInitContainers(
			buildOpsManagerAndBackupInitContainer(),
		),
		podtemplatespec.WithContainers(podtemplatespec.BuildContainer(
			podtemplatespec.WithContainerResources(defaultOpsManagerResourceRequirements()),
			podtemplatespec.WithContainerPorts(buildOpsManagerContainerPorts(stsHelper.HTTPSCertSecretName)),
			podtemplatespec.WithContainerPullPolicy(corev1.PullPolicy(envutil.ReadOrPanic(util.OpsManagerPullPolicy))),
			podtemplatespec.WithContainerImage(omImageURL),
			podtemplatespec.WithContainerEnvVars(stsHelper.EnvVars...),
			podtemplatespec.WithContainerEnvVars(getOpsManagerHTTPSEnvVars(stsHelper.HTTPSCertSecretName)...),
			podtemplatespec.WithContainerCommand([]string{"/opt/scripts/docker-entry-point.sh"}),
		)),
	}
	return func(podTemplateSpec *corev1.PodTemplateSpec) {
		for _, opt := range modificationFunctions {
			opt(podTemplateSpec)
		}
	}
}

// buildOpsManagerAndBackupInitContainer creates the init container which
// copies the entry point script in the OM/Backup container
func buildOpsManagerAndBackupInitContainer() corev1.Container {
	version := envutil.ReadOrDefault(util.InitOpsManagerVersion, "latest")
	initContainerImageURL := fmt.Sprintf("%s:%s", envutil.ReadOrPanic(util.InitOpsManagerImageUrl), version)
	return podtemplatespec.BuildContainer(
		podtemplatespec.WithContainerName(util.InitOpsManagerContainerName),
		podtemplatespec.WithContainerImage(initContainerImageURL),
	)
}

func buildOpsManagerContainerPorts(httpsCertSecretName string) []corev1.ContainerPort {
	return []corev1.ContainerPort{{ContainerPort: int32(getOpsManagerContainerPort(httpsCertSecretName))}}
}

// buildStatefulSet builds the StatefulSet of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources.
// This is a convenience method to pass all attributes inside a "parameters" object which is easier to
// build in client code and avoid passing too many different parameters to `buildStatefulSet`.
func buildStatefulSet(p StatefulSetHelper) (appsv1.StatefulSet, error) {
	template := buildMongoDBPodTemplateSpec(p)
	sts, err := createBaseDatabaseStatefulSetBuilder(p, template).Build()
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	if p.PodSpec != nil && p.PodSpec.PodTemplate != nil {
		return statefulset.MergeSpec(sts, &appsv1.StatefulSetSpec{Template: *p.PodSpec.PodTemplate})
	}
	return sts, err
}

// buildAppDbStatefulSet builds the StatefulSet for AppDB.
// It's mostly the same as the normal MongoDB one but has slightly different container and an additional mounting volume
func buildAppDbStatefulSet(p StatefulSetHelper) (appsv1.StatefulSet, error) {
	template := buildAppDBPodTemplateSpec(p)
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
	stsBuilder.AddVolume(
		corev1.Volume{
			Name: "appdb-scripts",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)
	stsBuilder.AddVolumeMount(
		"mongodb-enterprise-init-appdb",
		corev1.VolumeMount{
			Name:      "appdb-scripts",
			MountPath: "/opt/scripts",
			ReadOnly:  false,
		},
	)
	stsBuilder.AddVolumeMount(
		p.ContainerName,
		corev1.VolumeMount{
			Name:      "appdb-scripts",
			MountPath: "/opt/scripts",
			ReadOnly:  true,
		},
	)

	sts, err := stsBuilder.Build()
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	if p.PodSpec != nil && p.PodSpec.PodTemplate != nil {
		return statefulset.MergeSpec(sts, &appsv1.StatefulSetSpec{Template: *p.PodSpec.PodTemplate})
	}
	return sts, nil
}

// createBaseOpsManagerStatefulSetBuilder is the base method for building StatefulSet shared by Ops Manager and Backup Daemon.
// Shouldn't be called by end users directly
// Dev note: it's ok to move the different parts to parameters (pod spec could be an example) as the functionality
// evolves
func createBaseOpsManagerStatefulSetBuilder(p OpsManagerStatefulSetHelper, template corev1.PodTemplateSpec) (*statefulset.Builder, error) {
	labels := defaultPodLabels(p.StatefulSetHelperCommon)
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
	template, err := buildOpsManagerPodTemplateSpec(p)
	if err != nil {
		return appsv1.StatefulSet{}, err
	}

	builder, err := createBaseOpsManagerStatefulSetBuilder(p, template)
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	sts, err := builder.Build()
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	if p.StatefulSetConfiguration != nil {
		sts, err = statefulset.MergeSpec(sts, &p.StatefulSetConfiguration.Spec)
		if err != nil {
			return appsv1.StatefulSet{}, nil
		}
	}
	if err = setJvmArgsEnvVars(p.Spec, &sts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return sts, nil
}

// setJvmArgsEnvVars sets the correct environment variables for JVM size parameters.
// This method must be invoked on the final version of the StatefulSet (after user statefulSet spec
// was merged)
func setJvmArgsEnvVars(m mdbv1.MongoDBOpsManagerSpec, sts *appsv1.StatefulSet) error {
	jvmParamsEnvVars, err := buildJvmParamsEnvVars(m, sts.Spec.Template)
	if err != nil {
		return err
	}
	// pass Xmx java parameter to container (note, that we don't need to sort the env variables again
	// as the jvm params order is consistent)
	for _, envVar := range jvmParamsEnvVars {
		sts.Spec.Template.Spec.Containers[0].Env = append(sts.Spec.Template.Spec.Containers[0].Env, envVar)
	}
	return nil
}

func getOpsManagerHTTPSEnvVars(httpsSecretName string) []corev1.EnvVar {
	if httpsSecretName != "" {
		// Before creating the podTemplate, we need to add the new PemKeyFile
		// configuration if required.
		return []corev1.EnvVar{{
			Name:  mdbv1.ConvertNameToEnvVarFormat(util.MmsPEMKeyFile),
			Value: util.MmsPemKeyFileDirInContainer + "/server.pem",
		}}
	}
	return []corev1.EnvVar{}
}

func getOpsManagerContainerPort(httpsSecretName string) int {
	_, port := mdbv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		_, port = mdbv1.SchemePortFromAnnotation("https")
	}
	return port
}

func getURIScheme(httpsCertSecretName string) corev1.URIScheme {
	httpsSecretName := httpsCertSecretName
	scheme, _ := mdbv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		scheme, _ = mdbv1.SchemePortFromAnnotation("https")
	}
	return scheme
}

func buildLifecycle(preStopCmd []string) corev1.Lifecycle {
	return corev1.Lifecycle{
		PreStop: &corev1.Handler{
			Exec: &corev1.ExecAction{
				Command: preStopCmd,
			},
		},
	}
}

func buildBackupDaemonLifecycle() corev1.Lifecycle {
	return buildLifecycle([]string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"})
}

func buildOpsManagerLifecycle() corev1.Lifecycle {
	return buildLifecycle([]string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"})
}

func backupDaemonEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{{
		// For the OM Docker image to run as Backup Daemon, the BACKUP_DAEMON env variable
		// needs to be passed with any value.configureJvmParams
		Name:  util.ENV_BACKUP_DAEMON,
		Value: "backup",
	}}
}

// buildBackupDaemonStatefulSet builds the StatefulSet for backup daemon. It shares most of the configuration with
// OpsManager StatefulSet adding something on top of it
func buildBackupDaemonStatefulSet(p BackupStatefulSetHelper) (appsv1.StatefulSet, error) {
	template, err := buildBackupDaemonPodTemplateSpec(p)
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	stsBuilder, err := createBaseOpsManagerStatefulSetBuilder(p.OpsManagerStatefulSetHelper, template)
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

	sts, err := stsBuilder.Build()
	if err != nil {
		return appsv1.StatefulSet{}, err
	}

	if p.StatefulSetConfiguration != nil {
		sts, err = statefulset.MergeSpec(sts, &p.StatefulSetConfiguration.Spec)
		if err != nil {
			return appsv1.StatefulSet{}, nil
		}
	}
	// We need to calculate JVM memory parameters after the StatefulSet is merged
	// One idea for future: we can use the functional approach instead of Builder for Statefulset
	// and jvm mutation callbacks to the builder
	if err = setJvmArgsEnvVars(p.Spec, &sts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return sts, nil
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
		if helper.Security.TLSConfig.IsEnabled() {
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
		if helper.Security.GetAgentMechanism() == util.X509 {
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
		if helper.Security.GetInternalClusterAuthenticationMode() == util.X509 {
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
func buildService(namespacedName types.NamespacedName, owner mdbv1.CustomResourceReadWriter, label string, port int32, mongoServiceDefinition mdbv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
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

func baseOwnerReference(owner mdbv1.CustomResourceReadWriter) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, schema.GroupVersionKind{
			Group:   mdbv1.SchemeGroupVersion.Group,
			Version: mdbv1.SchemeGroupVersion.Version,
			Kind:    owner.GetObjectKind().GroupVersionKind().Kind,
		}),
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

func baseLivenessProbe() corev1.Probe {
	return corev1.Probe{
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

func baseAppDbLivenessProbe() corev1.Probe {
	baseProbe := baseLivenessProbe()
	baseProbe.Handler.Exec = &corev1.ExecAction{Command: []string{util.AppDbLivenessProbe}}
	return baseProbe
}

// opsManagerReadinessProbe creates the readiness probe.
// Note on 'PeriodSeconds': /monitor/health is a super lightweight method not doing any IO so we can make it more often.
func opsManagerReadinessProbe(scheme corev1.URIScheme) corev1.Probe {
	port := 8080
	if scheme == corev1.URISchemeHTTPS {
		port = 8443
	}
	return corev1.Probe{
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

func buildDatabaseReadinessProbe() corev1.Probe {
	return corev1.Probe{
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
func buildAppDbReadinessProbe() corev1.Probe {
	return corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.AppDbReadinessProbe}},
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

func databaseEnvVars(podVars *PodVars) []corev1.EnvVar {
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
		envVarFromSecret(util.ENV_VAR_AGENT_API_KEY, agentApiKeySecretName(podVars.ProjectID), util.OmAgentApiKey),
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
		trustedCACertLocation := path.Join(CaCertMountPath, util.CaCertMMS)
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
