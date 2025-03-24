package construct

import (
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/persistentvolumeclaim"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// pvcFunc convenience function to build a PersistentVolumeClaim. It accepts two config parameters - the one specified by
// the customers and the default one configured by the Operator. Putting the default one to the signature ensures the
// calling code doesn't forget to think about default values in case the user hasn't provided values.
func pvcFunc(name string, config *mdbv1.PersistenceConfig, defaultConfig mdbv1.PersistenceConfig, labels map[string]string) persistentvolumeclaim.Modification {
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

func createClaimsAndMountsMultiModeFunc(persistence *mdbv1.Persistence, defaultConfig mdbv1.MultiplePersistenceConfig, labels map[string]string) (map[string]persistentvolumeclaim.Modification, []corev1.VolumeMount) {
	mounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData),
		statefulset.CreateVolumeMount(util.PvcNameJournal, util.PvcMountPathJournal),
		statefulset.CreateVolumeMount(util.PvcNameLogs, util.PvcMountPathLogs),
	}
	return map[string]persistentvolumeclaim.Modification{
		util.PvcNameData:    pvcFunc(util.PvcNameData, persistence.MultipleConfig.Data, *defaultConfig.Data, labels),
		util.PvcNameJournal: pvcFunc(util.PvcNameJournal, persistence.MultipleConfig.Journal, *defaultConfig.Journal, labels),
		util.PvcNameLogs:    pvcFunc(util.PvcNameLogs, persistence.MultipleConfig.Logs, *defaultConfig.Logs, labels),
	}, mounts
}

func createClaimsAndMountsSingleModeFunc(config *mdbv1.PersistenceConfig, opts DatabaseStatefulSetOptions) (map[string]persistentvolumeclaim.Modification, []corev1.VolumeMount) {
	mounts := []corev1.VolumeMount{
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathData, statefulset.WithSubPath(util.PvcNameData)),
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathJournal, statefulset.WithSubPath(util.PvcNameJournal)),
		statefulset.CreateVolumeMount(util.PvcNameData, util.PvcMountPathLogs, statefulset.WithSubPath(util.PvcNameLogs)),
	}
	return map[string]persistentvolumeclaim.Modification{
		util.PvcNameData: pvcFunc(util.PvcNameData, config, *opts.PodSpec.Default.Persistence.SingleConfig, opts.Labels),
	}, mounts
}
