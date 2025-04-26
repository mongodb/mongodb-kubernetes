package construct

import (
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// SearchSourceDBResource is an object wrapping a MongoDBCommunity object
// Its purpose is to:
//   - isolate and identify all the data we need to get from the CR in order to reconcile search resources
//   - implement search reconcile logic in a generic way that is working for any types of MongoDB databases (all database CRs).
type SearchSourceDBResource interface {
	Name() string
	NamespacedName() types.NamespacedName
	KeyfileSecretName() string
	GetNamespace() string
	HasSeparateDataAndLogsVolumes() bool
	DatabaseServiceName() string
	DatabasePort() int
	GetMongoDBVersion() string
	IsSecurityTLSConfigEnabled() bool
}

func NewSearchSourceDBResourceFromMongoDBCommunity(mdbc *mdbcv1.MongoDBCommunity) SearchSourceDBResource {
	return &mdbcSearchResource{db: mdbc}
}

type mdbcSearchResource struct {
	db *mdbcv1.MongoDBCommunity
}

func (r *mdbcSearchResource) Name() string {
	return r.db.Name
}

func (r *mdbcSearchResource) NamespacedName() types.NamespacedName {
	return r.db.NamespacedName()
}

func (r *mdbcSearchResource) KeyfileSecretName() string {
	return r.db.GetAgentKeyfileSecretNamespacedName().Name
}

func (r *mdbcSearchResource) GetNamespace() string {
	return r.db.Namespace
}

func (r *mdbcSearchResource) HasSeparateDataAndLogsVolumes() bool {
	return r.db.HasSeparateDataAndLogsVolumes()
}

func (r *mdbcSearchResource) DatabaseServiceName() string {
	return r.db.ServiceName()
}

func (r *mdbcSearchResource) GetMongoDBVersion() string {
	return r.db.Spec.Version
}

func (r *mdbcSearchResource) IsSecurityTLSConfigEnabled() bool {
	return r.db.Spec.Security.TLS.Enabled
}

func (r *mdbcSearchResource) DatabasePort() int {
	return r.db.GetMongodConfiguration().GetDBPort()
}

// ReplicaSetOptions returns a set of options which will configure a ReplicaSet StatefulSet
func CreateSearchStatefulSetFunc(mdbSearch *searchv1.MongoDBSearch, sourceDBResource SearchSourceDBResource) statefulset.Modification {
	labels := map[string]string{
		"app": mdbSearch.SearchServiceNamespacedName().Name,
	}

	tmpVolume := statefulset.CreateVolumeFromEmptyDir("tmp")
	tmpVolumeMount := statefulset.CreateVolumeMount(tmpVolume.Name, "/tmp", statefulset.WithReadOnly(false))

	dataVolumeName := "data"
	keyfileVolumeName := "keyfile"
	mongotConfigVolumeName := "config"

	pvcVolumeMount := statefulset.CreateVolumeMount(dataVolumeName, "/mongot/data", statefulset.WithSubPath("data"))

	keyfileVolume := statefulset.CreateVolumeFromSecret("keyfile", sourceDBResource.KeyfileSecretName())
	keyfileVolumeMount := statefulset.CreateVolumeMount(keyfileVolumeName, "/mongot/keyfile", statefulset.WithReadOnly(true))

	mongotConfigVolume := statefulset.CreateVolumeFromConfigMap("config", mdbSearch.MongotConfigConfigMapNamespacedName().Name)
	mongotConfigVolumeMount := statefulset.CreateVolumeMount(mongotConfigVolumeName, "/mongot/config", statefulset.WithReadOnly(true))

	var persistenceConfig *common.PersistenceConfig
	if mdbSearch.Spec.Persistence != nil && mdbSearch.Spec.Persistence.SingleConfig != nil {
		persistenceConfig = mdbSearch.Spec.Persistence.SingleConfig
	}

	defaultPersistenceConfig := common.PersistenceConfig{Storage: "10G"}
	dataVolumeClaim := statefulset.WithVolumeClaim(dataVolumeName, PvcFunc(dataVolumeName, persistenceConfig, defaultPersistenceConfig, nil))

	podSecurityContext, _ := podtemplatespec.WithDefaultSecurityContextsModifications()

	volumeMounts := []corev1.VolumeMount{
		pvcVolumeMount,
		keyfileVolumeMount,
		tmpVolumeMount,
		mongotConfigVolumeMount,
	}

	volumes := []corev1.Volume{
		tmpVolume,
		keyfileVolume,
		mongotConfigVolume,
	}

	return statefulset.Apply(
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
				podtemplatespec.WithContainer("mongodb-search", mongodbSearchContainer(mdbSearch, volumeMounts)),
			),
		),
	)
}

func mongodbSearchContainer(mdbSearch *searchv1.MongoDBSearch, volumeMounts []corev1.VolumeMount) container.Modification {
	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	return container.Apply(
		container.WithName("mongodb-search"),
		container.WithImage("268558157000.dkr.ecr.eu-west-1.amazonaws.com/mongot/community:bd5ac935fe03426d6080bbb34ac6df5350ba3193"),
		container.WithImagePullPolicy(corev1.PullAlways),
		container.WithReadinessProbe(probes.Apply(
			probes.WithTCPSocket("", intstr.FromInt32(mdbSearch.GetMongotPort())),
			probes.WithInitialDelaySeconds(20),
			probes.WithPeriodSeconds(10),
		)),
		container.WithResourceRequirements(*createSearchResourceRequirements(mdbSearch.Spec.ResourceRequirements)),
		container.WithVolumeMounts(volumeMounts),
		container.WithCommand([]string{"sh"}),
		container.WithArgs([]string{
			"-c",
			"/mongot-community/mongot --config /mongot/config/config.yml",
		}),
		containerSecurityContext,
	)
}

func createSearchResourceRequirements(requirements *corev1.ResourceRequirements) *corev1.ResourceRequirements {
	if requirements != nil {
		return requirements
	} else {
		return newSearchDefaultRequirements()
	}
}

func newSearchDefaultRequirements() *corev1.ResourceRequirements {
	return &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    ParseQuantityOrZero("2"),
			corev1.ResourceMemory: ParseQuantityOrZero("2G"),
		},
	}
}
