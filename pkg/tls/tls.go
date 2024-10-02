package tls

import (
	"fmt"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
)

type Mode string

const (
	Disabled              Mode = "disabled"
	Require               Mode = "requireTLS"
	Prefer                Mode = "preferTLS"
	Allow                 Mode = "allowTLS"
	ConfigMapVolumeCAName      = "secret-ca"
)

func GetTLSModeFromMongodConfig(config map[string]interface{}) Mode {
	// spec.Security.TLSConfig.IsEnabled() is true -> requireSSLMode
	if config == nil {
		return Require
	}
	mode := maputil.ReadMapValueAsString(config, "net", "tls", "mode")

	if mode == "" {
		mode = maputil.ReadMapValueAsString(config, "net", "ssl", "mode")
	}
	if mode == "" {
		return Require
	}

	return Mode(mode)
}

// ConfigureStatefulSet modifies the provided StatefulSet with the required volumes.
func ConfigureStatefulSet(sts *appsv1.StatefulSet, resourceName, prefix, ca string) {
	if sts == nil || resourceName == "" {
		return
	}
	// In this location the certificates will be linked -s into server.pem
	secretName := fmt.Sprintf("%s-cert", resourceName)
	if prefix != "" {
		// Certificates will be used from the secret with the corresponding prefix.
		secretName = fmt.Sprintf("%s-%s-cert-pem", prefix, resourceName)
	}

	secretVolume := statefulset.CreateVolumeFromSecret(util.SecretVolumeName, secretName)
	sts.Spec.Template.Spec.Containers[0].VolumeMounts = append(sts.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		MountPath: util.TLSCertMountPath,
		Name:      secretVolume.Name,
		ReadOnly:  true,
	})
	sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, secretVolume)

	if ca != "" {
		caVolume := statefulset.CreateVolumeFromConfigMap(ConfigMapVolumeCAName, ca)
		sts.Spec.Template.Spec.Containers[0].VolumeMounts = append(sts.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			MountPath: util.TLSCaMountPath,
			Name:      caVolume.Name,
			ReadOnly:  true,
		})
		sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, caVolume)
	}
}
