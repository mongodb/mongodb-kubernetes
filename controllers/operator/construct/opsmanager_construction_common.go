package construct

import (
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	corev1 "k8s.io/api/core/v1"
)

const (
	AppDBConnectionStringVolume = "mongodb-uri"
	AppDBConnectionStringPath   = "/mongodb-ops-manager/.mongodb-mms-connection-string"
)

func buildMmsMongoUriVolume(opts OpsManagerStatefulSetOptions) (corev1.Volume, corev1.VolumeMount) {
	mmsMongoUriVolume := statefulset.CreateVolumeFromSecret(AppDBConnectionStringVolume, opts.AppDBConnectionSecretName)
	mmsMongoUriVolumeMount := corev1.VolumeMount{
		Name:      mmsMongoUriVolume.Name,
		ReadOnly:  true,
		MountPath: AppDBConnectionStringPath,
	}

	return mmsMongoUriVolume, mmsMongoUriVolumeMount
}
