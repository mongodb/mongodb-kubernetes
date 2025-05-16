package construct

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/persistentvolumeclaim"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// PvcFunc convenience function to build a PersistentVolumeClaim. It accepts two config parameters - the one specified by
// the customers and the default one configured by the Operator. Putting the default one to the signature ensures the
// calling code doesn't forget to think about default values in case the user hasn't provided values.
func PvcFunc(name string, config *common.PersistenceConfig, defaultConfig common.PersistenceConfig, labels map[string]string) persistentvolumeclaim.Modification {
	selectorFunc := persistentvolumeclaim.NOOP()
	storageClassNameFunc := persistentvolumeclaim.NOOP()
	if config != nil {
		if config.LabelSelector != nil {
			selectorFunc = persistentvolumeclaim.WithLabelSelector(&config.LabelSelector.LabelSelector)
		}
		if config.StorageClass != nil {
			storageClassNameFunc = persistentvolumeclaim.WithStorageClassName(*config.StorageClass)
		}
	}
	return persistentvolumeclaim.Apply(
		persistentvolumeclaim.WithName(name),
		persistentvolumeclaim.WithAccessModes(corev1.ReadWriteOnce),
		persistentvolumeclaim.WithResourceRequests(buildStorageRequirements(config, defaultConfig)),
		persistentvolumeclaim.WithLabels(labels),
		selectorFunc,
		storageClassNameFunc,
	)
}

func createClaimsAndMountsMultiModeFunc(persistence *common.Persistence, defaultConfig common.MultiplePersistenceConfig, labels map[string]string) (map[string]persistentvolumeclaim.Modification, []corev1.VolumeMount) {
	mounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData),
		statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal),
		statefulset.CreateVolumeMount(util.PvcNameLogs, util.PvcMountPathLogs),
	}
	return map[string]persistentvolumeclaim.Modification{
		util.PvcNameData:    PvcFunc(util.PvcNameData, persistence.MultipleConfig.Data, *defaultConfig.Data, labels),
		util.PvcNameJournal: PvcFunc(util.PvcNameJournal, persistence.MultipleConfig.Journal, *defaultConfig.Journal, labels),
		util.PvcNameLogs:    PvcFunc(util.PvcNameLogs, persistence.MultipleConfig.Logs, *defaultConfig.Logs, labels),
	}, mounts
}

func createClaimsAndMountsSingleModeFunc(config *common.PersistenceConfig, opts DatabaseStatefulSetOptions) (map[string]persistentvolumeclaim.Modification, []corev1.VolumeMount) {
	mounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData, statefulset.WithSubPath(util.PvcNameData)),
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal)),
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathLogs, statefulset.WithSubPath(util.PvcNameLogs)),
	}
	return map[string]persistentvolumeclaim.Modification{
		util.PvcNameData: PvcFunc(util.PvcNameData, config, *opts.PodSpec.Default.Persistence.SingleConfig, opts.Labels),
	}, mounts
}
