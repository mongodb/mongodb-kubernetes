package search_controller

import (
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	MongotContainerName      = "mongot"
	SearchLivenessProbePath  = "/health"
	SearchReadinessProbePath = "/health" // Todo: Update this when search GA is available
)

// SearchSourceDBResource is an object wrapping a MongoDBCommunity object
// Its purpose is to:
//   - isolate and identify all the data we need to get from the CR in order to reconcile search resources
//   - implement search reconcile logic in a generic way that is working for any types of MongoDB databases (all database CRs).
//
// TODO check if we could use already existing interface (DbCommon, MongoDBStatefulSetOwner, etc.)
type SearchSourceDBResource interface {
	GetName() string
	NamespacedName() types.NamespacedName
	KeyfileSecretName() string
	GetNamespace() string
	DatabaseServiceName() string
	DatabasePort() int
	IsSecurityTLSConfigEnabled() bool
	TLSOperatorCASecretNamespacedName() types.NamespacedName
	Members() int
	Validate() error
}

// ReplicaSetOptions returns a set of options which will configure a ReplicaSet StatefulSet
func CreateSearchStatefulSetFunc(mdbSearch *searchv1.MongoDBSearch, sourceDBResource SearchSourceDBResource, searchImage string) statefulset.Modification {
	labels := map[string]string{
		"app": mdbSearch.SearchServiceNamespacedName().Name,
	}

	tmpVolume := statefulset.CreateVolumeFromEmptyDir("tmp")
	tmpVolumeMount := statefulset.CreateVolumeMount(tmpVolume.Name, "/tmp", statefulset.WithReadOnly(false))

	dataVolumeName := "data"
	keyfileVolumeName := "keyfile"
	sourceUserPasswordVolumeName := "password"
	mongotConfigVolumeName := "config"

	pvcVolumeMount := statefulset.CreateVolumeMount(dataVolumeName, "/mongot/data", statefulset.WithSubPath("data"))

	keyfileVolume := statefulset.CreateVolumeFromSecret(keyfileVolumeName, sourceDBResource.KeyfileSecretName())
	keyfileVolumeMount := statefulset.CreateVolumeMount(keyfileVolumeName, "/mongot/keyfile", statefulset.WithReadOnly(true))

	sourceUserPasswordVolume := statefulset.CreateVolumeFromSecret(sourceUserPasswordVolumeName, mdbSearch.SourceUserPasswordSecretRef().Name)
	sourceUserPasswordVolumeMount := statefulset.CreateVolumeMount(sourceUserPasswordVolumeName, "/mongot/sourceUserPassword", statefulset.WithReadOnly(true))

	mongotConfigVolume := statefulset.CreateVolumeFromConfigMap(mongotConfigVolumeName, mdbSearch.MongotConfigConfigMapNamespacedName().Name)
	mongotConfigVolumeMount := statefulset.CreateVolumeMount(mongotConfigVolumeName, "/mongot/config", statefulset.WithReadOnly(true))

	var persistenceConfig *common.PersistenceConfig
	if mdbSearch.Spec.Persistence != nil && mdbSearch.Spec.Persistence.SingleConfig != nil {
		persistenceConfig = mdbSearch.Spec.Persistence.SingleConfig
	}

	defaultPersistenceConfig := common.PersistenceConfig{Storage: "10G"}
	dataVolumeClaim := statefulset.WithVolumeClaim(dataVolumeName, construct.PvcFunc(dataVolumeName, persistenceConfig, defaultPersistenceConfig, nil))

	podSecurityContext, _ := podtemplatespec.WithDefaultSecurityContextsModifications()

	volumeMounts := []corev1.VolumeMount{
		pvcVolumeMount,
		keyfileVolumeMount,
		tmpVolumeMount,
		mongotConfigVolumeMount,
		sourceUserPasswordVolumeMount,
	}

	volumes := []corev1.Volume{
		tmpVolume,
		keyfileVolume,
		mongotConfigVolume,
		sourceUserPasswordVolume,
	}

	stsModifications := []statefulset.Modification{
		statefulset.WithName(mdbSearch.StatefulSetNamespacedName().Name),
		statefulset.WithNamespace(mdbSearch.StatefulSetNamespacedName().Namespace),
		statefulset.WithServiceName(mdbSearch.SearchServiceNamespacedName().Name),
		statefulset.WithLabels(labels),
		statefulset.WithOwnerReference(mdbSearch.GetOwnerReferences()),
		statefulset.WithMatchLabels(labels),
		statefulset.WithReplicas(1),
		statefulset.WithUpdateStrategyType(appsv1.RollingUpdateStatefulSetStrategyType),
		dataVolumeClaim,
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				podSecurityContext,
				podtemplatespec.WithPodLabels(labels),
				podtemplatespec.WithVolumes(volumes),
				podtemplatespec.WithServiceAccount(sourceDBResource.DatabaseServiceName()),
				podtemplatespec.WithServiceAccount(util.MongoDBServiceAccount),
				podtemplatespec.WithContainer(MongotContainerName, mongodbSearchContainer(mdbSearch, volumeMounts, searchImage)),
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

func mongodbSearchContainer(mdbSearch *searchv1.MongoDBSearch, volumeMounts []corev1.VolumeMount, searchImage string) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
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
			`
cp /mongot/keyfile/keyfile /tmp/keyfile
chown 2000:2000 /tmp/keyfile
chmod 0600 /tmp/keyfile

cp /mongot/sourceUserPassword/password /tmp/sourceUserPassword
chown 2000:2000 /tmp/sourceUserPassword
chmod 0600 /tmp/sourceUserPassword

/mongot-community/mongot --config /mongot/config/config.yml
`,
		}),
		containerSecurityContext,
	)
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
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    construct.ParseQuantityOrZero("2"),
			corev1.ResourceMemory: construct.ParseQuantityOrZero("2G"),
		},
	}
}
