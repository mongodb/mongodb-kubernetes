package searchcontroller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	MongotContainerName          = "mongot"
	MongotConfigFilename         = "config.yml"
	MongotConfigLeaderFilename   = "config-leader.yml"
	MongotConfigFollowerFilename = "config-follower.yml"
	MongotConfigDirPath          = "/mongot"
	MongotPerPodConfigDirPath    = "/mongot/startup-config"
	MongotConfigPath             = MongotConfigDirPath + "/" + MongotConfigFilename
	MongotDataPath               = "/mongot/data"
	MongotKeyfileFilename        = "keyfile"
	MongotKeyfilePath            = "/mongot/" + MongotKeyfileFilename
	tempVolumePath               = "/tmp"
	TempKeyfilePath              = tempVolumePath + "/" + MongotKeyfileFilename
	MongotSourceUserPasswordPath = "/mongot/sourceUserPassword" // #nosec G101 -- This is not a hardcoded password, just a path to a file containing the password
	TempSourceUserPasswordPath   = tempVolumePath + "/" + "sourceUserPassword"
	SearchLivenessProbePath      = "/health"
	SearchReadinessProbePath     = "/ready"
	tlsCACertName                = "ca.crt"

	// KeyFilePasswordSecretKey is the fixed key inside a dedicated keyFilePassword secret
	// (spec.*.keyFilePasswordSecretRef) holding the password that decrypts a password-encrypted
	// PEM private key.
	KeyFilePasswordSecretKey = "keyFilePassword" // #nosec G101 -- secret key name, not a password

	X509KeyPasswordMountPath        = "/mongot/x509-key-password"           // #nosec G101 -- path, not a password
	TempX509KeyPasswordPath         = tempVolumePath + "/x509-key-password" // #nosec G101 -- path, not a password
	X509ClientCertOperatorMountPath = "/var/lib/tls/x509-client/"

	ServerNamePlaceholder = "__SERVER_NAME__"

	// TempMongotConfigPath is the writable copy of the mongot config used at runtime.
	// The original is mounted read-only from a ConfigMap; we copy it to /tmp so that
	// sed can replace the server-name placeholder with the actual pod hostname.
	TempMongotConfigPath = tempVolumePath + "/" + MongotConfigFilename

	GrpcKeyPasswordMountPath = "/mongot/grpc-key-password"           // #nosec G101 -- path, not a password
	TempGrpcKeyPasswordPath  = tempVolumePath + "/grpc-key-password" // #nosec G101 -- path, not a password

	ScramClientCertOperatorMountPath = "/var/lib/tls/scram-client/"
	ScramKeyPasswordMountPath        = "/mongot/scram-key-password"           // #nosec G101 -- path, not a password
	TempScramKeyPasswordPath         = tempVolumePath + "/scram-key-password" // #nosec G101 -- path, not a password
)

// SearchSourceDBResource is an object wrapping a MongoDBCommunity object
// Its purpose is to:
//   - isolate and identify all the data we need to get from the CR in order to reconcile search resources
//   - implement search reconcile logic in a generic way that is working for any types of MongoDB databases (all database CRs).
type SearchSourceDBResource interface {
	KeyfileSecretName() string
	TLSConfig() *TLSSourceConfig
	HostSeeds(shardName string) ([]string, error)
	Validate() error
	ResourceType() mdb.ResourceType
}

// SearchSourceShardedDeployment extends SearchSourceDBResource for sharded MongoDB clusters.
type SearchSourceShardedDeployment interface {
	SearchSourceDBResource
	GetShardCount() int
	GetShardNames() []string
	GetUnmanagedLBEndpointForShard(shardName string) string
	MongosHostsAndPorts() []string
}

type TLSSourceConfig struct {
	CAFileName       string
	CAVolume         corev1.Volume
	ResourcesToWatch map[watch.Type][]types.NamespacedName
}

// CreateSearchStatefulSetFunc returns a statefulset.Modification that configures a mongot StatefulSet.
// It works for both non-sharded and per-shard deployments.
//
// sizing is the resolved per-(cluster, shard) ClusterSpec — see
// MongoDBSearch.ResolveSizingForClusterShard — read for Replicas / Persistence /
// ResourceRequirements / JVMFlags / StatefulSetConfiguration.
func CreateSearchStatefulSetFunc(mdbSearch *searchv1.MongoDBSearch, sizing searchv1.ClusterSpec, stsName, namespace, svcName, configMapName string, labels map[string]string, searchImage string, usePerPodConfig bool) statefulset.Modification {
	tmpVolume := statefulset.CreateVolumeFromEmptyDir("tmp")
	tmpVolumeMount := statefulset.CreateVolumeMount(tmpVolume.Name, tempVolumePath, statefulset.WithReadOnly(false))

	dataVolumeName := "data"
	mongotConfigVolumeName := "config"

	pvcVolumeMount := statefulset.CreateVolumeMount(dataVolumeName, MongotDataPath, statefulset.WithSubPath("data"))

	mongotConfigVolume := statefulset.CreateVolumeFromConfigMap(mongotConfigVolumeName, configMapName)

	var mongotConfigVolumeMount corev1.VolumeMount
	if usePerPodConfig {
		mongotConfigVolumeMount = statefulset.CreateVolumeMount(mongotConfigVolumeName, MongotPerPodConfigDirPath, statefulset.WithReadOnly(true))
	} else {
		mongotConfigVolumeMount = statefulset.CreateVolumeMount(mongotConfigVolumeName, MongotConfigPath, statefulset.WithReadOnly(true), statefulset.WithSubPath(MongotConfigFilename))
	}

	var persistenceConfig *v1.PersistenceConfig
	if sizing.Persistence != nil && sizing.Persistence.SingleConfig != nil {
		persistenceConfig = sizing.Persistence.SingleConfig
	}

	defaultPersistenceConfig := v1.PersistenceConfig{Storage: util.DefaultMongodStorageSize}
	dataVolumeClaim := statefulset.WithVolumeClaim(dataVolumeName, construct.PvcFunc(dataVolumeName, persistenceConfig, defaultPersistenceConfig, nil))

	podSecurityContext, _ := podtemplatespec.WithDefaultSecurityContextsModifications()

	volumeMounts := []corev1.VolumeMount{
		pvcVolumeMount,
		tmpVolumeMount,
		mongotConfigVolumeMount,
	}

	volumes := []corev1.Volume{
		tmpVolume,
		mongotConfigVolume,
	}

	stsModifications := []statefulset.Modification{
		statefulset.WithName(stsName),
		statefulset.WithNamespace(namespace),
		statefulset.WithServiceName(svcName),
		statefulset.WithLabels(labels),
		statefulset.WithMatchLabels(labels),
		statefulset.WithReplicas(sizing.ReplicasOrDefault()),
		statefulset.WithUpdateStrategyType(appsv1.RollingUpdateStatefulSetStrategyType),
		dataVolumeClaim,
		withDataPVCRetentionPolicy(),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podSecurityContext,
				podtemplatespec.WithPodLabels(labels),
				podtemplatespec.WithVolumes(volumes),
				podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
				podtemplatespec.WithTerminationGracePeriodSeconds(int(searchv1.MongotTerminationGracePeriodSeconds)),
				// Default: spread this StatefulSet's mongot pods across hosts (preferred, not
				// required). A clusters[].statefulSet affinity override replaces this term.
				podtemplatespec.WithAffinity(labels[appLabelKey], appLabelKey, 100),
				podtemplatespec.WithTopologyKey(util.DefaultAntiAffinityTopologyKey, 0),
				podtemplatespec.WithContainer(MongotContainerName, mongodbSearchContainer(mdbSearch, sizing, volumeMounts, searchImage, usePerPodConfig)),
			),
		),
	}

	return statefulset.Apply(stsModifications...)
}

// withDataPVCRetentionPolicy reclaims the mongot index PVC on StatefulSet delete
// (CR removed) and on scale-down. The index is rebuildable, so freeing the storage
// immediately is safe — a later scale-up reindexes from mongod.
func withDataPVCRetentionPolicy() statefulset.Modification {
	return func(sts *appsv1.StatefulSet) {
		sts.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		}
	}
}

// StatefulSetOverrideModification applies the resolved clusters[].statefulSet, with any
// shardOverrides[].statefulSet already deep-merged in (see ResolveSizingForClusterShard).
// It must run LAST in the modification chain, over the fully built StatefulSet: the override
// merge sorts volumes by name, so merging mid-pipeline (before the password/TLS
// volume modifications append) yields a different volume order on the create and
// update paths — the first reconcile after STS creation then sees a spurious
// template diff and rolls every mongot pod. Applying last also makes the user
// override win over operator-set fields, as the CRD field documents.
func StatefulSetOverrideModification(stsConfig *v1.StatefulSetConfiguration) statefulset.Modification {
	if stsConfig == nil {
		return statefulset.NOOP()
	}
	return statefulset.Apply(
		statefulset.WithCustomSpecs(stsConfig.SpecWrapper.Spec),
		statefulset.WithObjectMetadata(
			stsConfig.MetadataWrapper.Labels,
			stsConfig.MetadataWrapper.Annotations,
		),
	)
}

// PasswordAuthModification returns a statefulset.Modification that mounts the password secret
// and sets up the file permissions workaround for password-based sync source authentication.
func PasswordAuthModification(mdbSearch *searchv1.MongoDBSearch) statefulset.Modification {
	sourceUserPasswordVolumeName := "password"
	sourceUserPasswordSecretKey := mdbSearch.SourceUserPasswordSecretRef()
	sourceUserPasswordVolume := statefulset.CreateVolumeFromSecret(sourceUserPasswordVolumeName, sourceUserPasswordSecretKey.Name)
	sourceUserPasswordVolumeMount := statefulset.CreateVolumeMount(sourceUserPasswordVolumeName, MongotSourceUserPasswordPath, statefulset.WithReadOnly(true), statefulset.WithSubPath(sourceUserPasswordSecretKey.Key))

	return statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		podtemplatespec.WithVolume(sourceUserPasswordVolume),
		podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			container.WithVolumeMounts([]corev1.VolumeMount{sourceUserPasswordVolumeMount}),
			prependCommand(sensitiveFilePermissionsWorkaround(MongotSourceUserPasswordPath, TempSourceUserPasswordPath, "0600")),
		)),
	))
}

func CreateKeyfileModificationFunc(keyfileSecretName string) statefulset.Modification {
	keyfileVolumeName := "keyfile"
	keyfileVolume := statefulset.CreateVolumeFromSecret(keyfileVolumeName, keyfileSecretName)
	keyfileVolumeMount := statefulset.CreateVolumeMount(keyfileVolumeName, MongotKeyfilePath, statefulset.WithReadOnly(true), statefulset.WithSubPath(MongotKeyfileFilename))

	return statefulset.Apply(
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podtemplatespec.WithVolumes([]corev1.Volume{keyfileVolume}),
				podtemplatespec.WithContainer(MongotContainerName,
					container.Apply(
						container.WithVolumeMounts([]corev1.VolumeMount{keyfileVolumeMount}),
						prependCommand(sensitiveFilePermissionsWorkaround(MongotKeyfilePath, TempKeyfilePath, "0600")),
					),
				),
			),
		),
	)
}

// jvmFlags builds the --jvm-flags argument from the per-cluster user-provided
// JVMFlags slice plus a default heap-size pair derived from memory requests.
func jvmFlags(userJVMFlags []string, resourceRequirements corev1.ResourceRequirements) string {
	flags := []string{}

	var heapConfigured bool
	for _, jvmFlag := range userJVMFlags {
		if strings.HasPrefix(jvmFlag, "-Xms") || strings.HasPrefix(jvmFlag, "-Xmx") {
			heapConfigured = true
			break
		}
	}
	// it's recommended to set the minimum heap size (-Xms) and maximum heap size (-Xmx) to the same value
	// but if any of them are provided by the users we are not setting defaults. Only set defaults if
	// none of them are provided.
	if !heapConfigured {
		// in this document we are recommended to set the half of memory to the JVM heap https://www.mongodb.com/docs/manual/tutorial/mongot-sizing/advanced-guidance/hardware/#jvm-heap-sizing
		// so we should do that even if the jvm flags are not configured by users.
		memRequest := resourceRequirements.Requests.Memory()
		halfBytes := memRequest.Value() / 2
		halfMB := halfBytes / (1024 * 1024)
		flags = append(flags, fmt.Sprintf("-Xmx%dm", halfMB))
		flags = append(flags, fmt.Sprintf("-Xms%dm", halfMB))
	}

	allFlags := append(flags, userJVMFlags...)
	flagsValue := strings.Join(allFlags, " ")
	return fmt.Sprintf(`--jvm-flags "%s"`, flagsValue)
}

func mongodbSearchContainer(mdbSearch *searchv1.MongoDBSearch, perCluster searchv1.ClusterSpec, volumeMounts []corev1.VolumeMount, searchImage string, usePerPodConfig bool) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	resourceRequirements := createSearchResourceRequirements(perCluster.ResourceRequirements)

	jvmFlags := jvmFlags(perCluster.JVMFlags, resourceRequirements)

	var mongotStartCommand string
	if usePerPodConfig {
		mongotStartCommand = mongotPerPodConfigStartCommand(jvmFlags)
	} else {
		// Copy config from read-only ConfigMap mount to writable /tmp, replace the
		// server-name placeholder with the actual pod hostname, then start mongot.
		mongotStartCommand = fmt.Sprintf(`cp %s %s
sed -i "s/%s/$HOSTNAME/" %s
/mongot-community/mongot --config %s %s`, MongotConfigPath, TempMongotConfigPath, ServerNamePlaceholder, TempMongotConfigPath, TempMongotConfigPath, jvmFlags)
	}

	return container.Apply(
		container.WithName(MongotContainerName),
		container.WithImage(searchImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithLivenessProbe(mongotLivenessProbe(mdbSearch)),
		container.WithReadinessProbe(mongotReadinessProbe(mdbSearch)),
		container.WithResourceRequirements(resourceRequirements),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand([]string{"sh"}),
		container.WithArgs([]string{
			"-c",
			mongotStartCommand,
		}),
		containerSecurityContext,
	)
}

// mongotPerPodConfigStartCommand returns the shell script that reads the pod's role from ConfigMap.
func mongotPerPodConfigStartCommand(jvmFlags string) string {
	// Copy the role-specific config from the read-only ConfigMap mount to writable /tmp,
	// replace the server-name placeholder with the actual pod hostname, then start mongot.
	return fmt.Sprintf(`ROLE=$(cat "%s/$HOSTNAME")
cp "%s/config-${ROLE}.yml" %s
sed -i "s/%s/$HOSTNAME/" %s
/mongot-community/mongot --config %s %s`,
		MongotPerPodConfigDirPath, MongotPerPodConfigDirPath, TempMongotConfigPath, ServerNamePlaceholder, TempMongotConfigPath, TempMongotConfigPath, jvmFlags)
}

func mongotLivenessProbe(search *searchv1.MongoDBSearch) func(*corev1.Probe) {
	return probes.Apply(
		probes.WithHandler(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Scheme: corev1.URISchemeHTTP,
				Port:   intstr.FromInt32(search.GetMongotHealthCheckPort()),
				Path:   SearchLivenessProbePath,
			},
		}),
		probes.WithInitialDelaySeconds(10),
		probes.WithPeriodSeconds(10),
		probes.WithTimeoutSeconds(5),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(10),
	)
}

// mongotReadinessProbe just uses the endpoint intended for liveness checks;
// readiness check endpoint may be available in search GA.
func mongotReadinessProbe(search *searchv1.MongoDBSearch) func(*corev1.Probe) {
	return probes.Apply(
		probes.WithHandler(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Scheme: corev1.URISchemeHTTP,
				Port:   intstr.FromInt32(search.GetMongotHealthCheckPort()),
				Path:   SearchReadinessProbePath,
			},
		}),
		probes.WithInitialDelaySeconds(20),
		probes.WithPeriodSeconds(10),
		probes.WithTimeoutSeconds(5),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(3),
	)
}

func createSearchResourceRequirements(userRequirements *corev1.ResourceRequirements) corev1.ResourceRequirements {
	defaults := newSearchDefaultRequirements()
	if userRequirements == nil {
		return defaults
	}

	// Default into a copy: userRequirements may point into the live CR spec
	// (cluster or shardOverride entry), which must never be mutated.
	userRequirements = userRequirements.DeepCopy()

	if userRequirements.Requests == nil {
		userRequirements.Requests = defaults.Requests
		return *userRequirements
	}

	if userRequirements.Requests.Memory().IsZero() {
		userRequirements.Requests[corev1.ResourceMemory] = defaults.Requests[corev1.ResourceMemory]
	}
	if userRequirements.Requests.Cpu().IsZero() {
		userRequirements.Requests[corev1.ResourceCPU] = defaults.Requests[corev1.ResourceCPU]
	}

	return *userRequirements
}

func newSearchDefaultRequirements() corev1.ResourceRequirements {
	// according to the document https://www.mongodb.com/docs/manual/tutorial/mongot-sizing/quick-start/, we should use
	// a small or medium High-CPU node for general use cases. That's what we return from here.
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    construct.ParseQuantityOrZero("2"),
			corev1.ResourceMemory: construct.ParseQuantityOrZero("4Gi"),
		},
	}
}

// The container command is set to "sh" and args is ["-c", "<script>"]
// this modifies the second argument to prepend a command to the script
// a new line is always inserted after the prepended command
func prependCommand(commands string) container.Modification {
	return func(c *corev1.Container) {
		c.Args[1] = fmt.Sprintf("%s\n%s", commands, c.Args[1])
	}
}

// mongot requires certain senstive files to have 600 permissions
// but we can't get secret subPaths to have those permissions directly
// so we copy them to a temp folder and set the permissions there
func sensitiveFilePermissionsWorkaround(filePath, tempFilePath, fileMode string) string {
	return fmt.Sprintf(`
cp %[1]s %[2]s
chown 2000:2000 %[2]s
chmod %[3]s %[2]s
`, filePath, tempFilePath, fileMode)
}

func sensitiveFilePermissionsForAPIKeys(srcFilePath, destFilePath, fileMode string) string {
	return fmt.Sprintf(`
cp %[1]s/* %[2]s
chmod %[3]s %[2]s/*
`, srcFilePath, destFilePath, fileMode)
}
