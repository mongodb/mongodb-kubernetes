package searchcontroller

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	MongotContainerName          = "mongot"
	MongotConfigFilename         = "config.yml"
	MongotConfigLeaderFilename   = "config-leader.yml"
	MongotConfigFollowerFilename = "config-follower.yml"
	MongotConfigDirPath          = "/mongot"
	MongotConfigPath             = MongotConfigDirPath + "/" + MongotConfigFilename
	MongotDataPath               = "/mongot/data"
	MongotKeyfileFilename        = "keyfile"
	MongotKeyfilePath            = "/mongot/" + MongotKeyfileFilename
	tempVolumePath               = "/tmp"
	TempKeyfilePath              = tempVolumePath + "/" + MongotKeyfileFilename
	MongotSourceUserPasswordPath = "/mongot/sourceUserPassword" // #nosec G101 -- This is not a hardcoded password, just a path to a file containing the password
	TempSourceUserPasswordPath   = tempVolumePath + "/" + "sourceUserPassword"
	SearchLivenessProbePath      = "/health"
	SearchReadinessProbePath     = "/health" // Todo: Update this when search GA is available
	tlsCACertName                = "ca.crt"
)

// SearchSourceDBResource is an object wrapping a MongoDBCommunity object
// Its purpose is to:
//   - isolate and identify all the data we need to get from the CR in order to reconcile search resources
//   - implement search reconcile logic in a generic way that is working for any types of MongoDB databases (all database CRs).
type SearchSourceDBResource interface {
	KeyfileSecretName() string
	TLSConfig() *TLSSourceConfig
	HostSeeds(shardName string) []string
	Validate() error
	ResourceType() mdb.ResourceType
}

// SearchSourceShardedDeployment extends SearchSourceDBResource for sharded MongoDB clusters.
type SearchSourceShardedDeployment interface {
	SearchSourceDBResource
	GetShardCount() int
	GetShardNames() []string
	GetUnmanagedLBEndpointForShard(shardName string) string
	MongosHostAndPort() string
}

type TLSSourceConfig struct {
	CAFileName       string
	CAVolume         corev1.Volume
	ResourcesToWatch map[watch.Type][]types.NamespacedName
}

// CreateSearchStatefulSetFunc returns a statefulset.Modification that configures a mongot StatefulSet.
// It works for both non-sharded and per-shard deployments, the caller is responsible for providing the appropriate names.
// When usePerPodConfig is true, the ConfigMap is mounted as a directory and an entrypoint script
// selects the appropriate config file (leader vs follower) based on the pod's ordinal.
func CreateSearchStatefulSetFunc(mdbSearch *searchv1.MongoDBSearch, stsName, namespace, svcName, configMapName string, labels map[string]string, searchImage string, usePerPodConfig bool) statefulset.Modification {
	tmpVolume := statefulset.CreateVolumeFromEmptyDir("tmp")
	tmpVolumeMount := statefulset.CreateVolumeMount(tmpVolume.Name, tempVolumePath, statefulset.WithReadOnly(false))

	dataVolumeName := "data"
	sourceUserPasswordVolumeName := "password"
	mongotConfigVolumeName := "config"

	pvcVolumeMount := statefulset.CreateVolumeMount(dataVolumeName, MongotDataPath, statefulset.WithSubPath("data"))

	sourceUserPasswordSecretKey := mdbSearch.SourceUserPasswordSecretRef()
	sourceUserPasswordVolume := statefulset.CreateVolumeFromSecret(sourceUserPasswordVolumeName, sourceUserPasswordSecretKey.Name)
	sourceUserPasswordVolumeMount := statefulset.CreateVolumeMount(sourceUserPasswordVolumeName, MongotSourceUserPasswordPath, statefulset.WithReadOnly(true), statefulset.WithSubPath(sourceUserPasswordSecretKey.Key))

	mongotConfigVolume := statefulset.CreateVolumeFromConfigMap(mongotConfigVolumeName, configMapName)

	// When usePerPodConfig is true, mount the ConfigMap as a directory so the entrypoint script
	// can select the appropriate config file based on pod ordinal.
	// When false, use SubPath to mount only the single config file.
	var mongotConfigVolumeMount corev1.VolumeMount
	if usePerPodConfig {
		mongotConfigVolumeMount = statefulset.CreateVolumeMount(mongotConfigVolumeName, MongotConfigDirPath, statefulset.WithReadOnly(true))
	} else {
		mongotConfigVolumeMount = statefulset.CreateVolumeMount(mongotConfigVolumeName, MongotConfigPath, statefulset.WithReadOnly(true), statefulset.WithSubPath(MongotConfigFilename))
	}

	var persistenceConfig *common.PersistenceConfig
	if mdbSearch.Spec.Persistence != nil && mdbSearch.Spec.Persistence.SingleConfig != nil {
		persistenceConfig = mdbSearch.Spec.Persistence.SingleConfig
	}

	defaultPersistenceConfig := common.PersistenceConfig{Storage: util.DefaultMongodStorageSize}
	dataVolumeClaim := statefulset.WithVolumeClaim(dataVolumeName, construct.PvcFunc(dataVolumeName, persistenceConfig, defaultPersistenceConfig, nil))

	podSecurityContext, _ := podtemplatespec.WithDefaultSecurityContextsModifications()

	volumeMounts := []corev1.VolumeMount{
		pvcVolumeMount,
		tmpVolumeMount,
		mongotConfigVolumeMount,
		sourceUserPasswordVolumeMount,
	}

	volumes := []corev1.Volume{
		tmpVolume,
		mongotConfigVolume,
		sourceUserPasswordVolume,
	}

	stsModifications := []statefulset.Modification{
		statefulset.WithName(stsName),
		statefulset.WithNamespace(namespace),
		statefulset.WithServiceName(svcName),
		statefulset.WithLabels(labels),
		statefulset.WithOwnerReference(mdbSearch.GetOwnerReferences()),
		statefulset.WithMatchLabels(labels),
		statefulset.WithReplicas(mdbSearch.GetReplicas()),
		statefulset.WithUpdateStrategyType(appsv1.RollingUpdateStatefulSetStrategyType),
		dataVolumeClaim,
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podSecurityContext,
				podtemplatespec.WithPodLabels(labels),
				podtemplatespec.WithVolumes(volumes),
				podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
				podtemplatespec.WithContainer(MongotContainerName, mongodbSearchContainer(mdbSearch, volumeMounts, searchImage, usePerPodConfig)),
			),
		),
	}

	if mdbSearch.Spec.StatefulSetConfiguration != nil {
		stsModifications = append(stsModifications, statefulset.WithCustomSpecs(mdbSearch.Spec.StatefulSetConfiguration.SpecWrapper.Spec))
		stsModifications = append(stsModifications, statefulset.WithObjectMetadata(
			mdbSearch.Spec.StatefulSetConfiguration.MetadataWrapper.Labels,
			mdbSearch.Spec.StatefulSetConfiguration.MetadataWrapper.Annotations,
		))
	}

	return statefulset.Apply(stsModifications...)
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

func mongodbSearchContainer(mdbSearch *searchv1.MongoDBSearch, volumeMounts []corev1.VolumeMount, searchImage string, usePerPodConfig bool) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	// When usePerPodConfig is true, use an entrypoint script that selects the config file
	// based on the pod's ordinal (pod-0 is leader, others are followers).
	var mongotStartCommand string
	if usePerPodConfig {
		mongotStartCommand = mongotPerPodConfigStartCommand()
	} else {
		mongotStartCommand = fmt.Sprintf("/mongot-community/mongot --config %s", MongotConfigPath)
	}

	return container.Apply(
		container.WithName(MongotContainerName),
		container.WithImage(searchImage),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithLivenessProbe(mongotLivenessProbe(mdbSearch)),
		container.WithReadinessProbe(mongotReadinessProbe(mdbSearch)),
		container.WithResourceRequirements(createSearchResourceRequirements(mdbSearch.Spec.ResourceRequirements)),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand([]string{"sh"}),
		container.WithArgs([]string{
			"-c",
			mongotStartCommand,
		}),
		prependCommand(sensitiveFilePermissionsWorkaround(MongotSourceUserPasswordPath, TempSourceUserPasswordPath, "0600")),
		containerSecurityContext,
	)
}

// mongotPerPodConfigStartCommand returns the shell script that selects the appropriate
// config file based on pod name lookup. The ConfigMap contains an entry for each pod
// with its role (leader/follower), so the script simply reads the role from the file.
func mongotPerPodConfigStartCommand() string {
	return fmt.Sprintf(`ROLE=$(cat "%s/$(hostname)")
/mongot-community/mongot --config %s/config-${ROLE}.yml`,
		MongotConfigDirPath, MongotConfigDirPath)
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

func createSearchResourceRequirements(requirements *corev1.ResourceRequirements) corev1.ResourceRequirements {
	if requirements != nil {
		return *requirements
	} else {
		return newSearchDefaultRequirements()
	}
}

func newSearchDefaultRequirements() corev1.ResourceRequirements {
	// TODO: add default limits once there is an official mongot sizing guide
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    construct.ParseQuantityOrZero("2"),
			corev1.ResourceMemory: construct.ParseQuantityOrZero("2G"),
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
