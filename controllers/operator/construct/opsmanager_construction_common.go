package construct

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/statefulset"
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
