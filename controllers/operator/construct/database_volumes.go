package construct

import (
	"fmt"
	"path"

	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/pkg/statefulset"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

type MongoDBVolumeSource interface {
	GetVolumes() []corev1.Volume
	GetVolumeMounts() []corev1.VolumeMount
	GetEnvs() []corev1.EnvVar
	ShouldBeAdded() bool
}

type caVolumeSource struct {
	opts   DatabaseStatefulSetOptions
	logger *zap.SugaredLogger
}

func (c *caVolumeSource) GetVolumes() []corev1.Volume {
	return []corev1.Volume{statefulset.CreateVolumeFromConfigMap(CaCertName, c.opts.PodVars.SSLMMSCAConfigMap)}
}

func (c *caVolumeSource) GetVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			MountPath: caCertMountPath,
			Name:      CaCertName,
			ReadOnly:  true,
		},
	}
}

func (c *caVolumeSource) GetEnvs() []corev1.EnvVar {
	// A custom CA has been provided, point the trusted CA to the location of custom CAs
	trustedCACertLocation := path.Join(caCertMountPath, util.CaCertMMS)
	return []corev1.EnvVar{
		{
			Name:  util.EnvVarSSLTrustedMMSServerCertificate,
			Value: trustedCACertLocation,
		},
	}
}

func (c *caVolumeSource) ShouldBeAdded() bool {
	return c.opts.PodVars != nil && c.opts.PodVars.SSLMMSCAConfigMap != ""
}

// tlsVolumeSource provides the volume and volumeMounts that need to be created for TLS.
type tlsVolumeSource struct {
	security     *mdbv1.Security
	databaseOpts DatabaseStatefulSetOptions
	logger       *zap.SugaredLogger
}

func (c *tlsVolumeSource) getVolumesAndMounts() ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	security := c.security
	databaseOpts := c.databaseOpts

	// We default each value to the "old-design"
	tlsConfig := security.TLSConfig
	if !security.IsTLSEnabled() {
		return volumes, volumeMounts
	}

	secretName := security.MemberCertificateSecretName(databaseOpts.Name)

	caName := fmt.Sprintf("%s-ca", databaseOpts.Name)
	if tlsConfig != nil && tlsConfig.CA != "" {
		caName = tlsConfig.CA
	} else {
		c.logger.Debugf("No CA name has been supplied, defaulting to: %s", caName)
	}

	// This two functions modify the volumes to be optional (the absence of the referenced
	// secret/configMap do not prevent the pods from starting)
	optionalSecretFunc := func(v *corev1.Volume) { v.Secret.Optional = util.BooleanRef(true) }
	optionalConfigMapFunc := func(v *corev1.Volume) { v.ConfigMap.Optional = util.BooleanRef(true) }

	secretMountPath := util.TLSCertMountPath
	configmapMountPath := util.TLSCaMountPath
	volumeSecretName := fmt.Sprintf("%s%s", secretName, certs.OperatorGeneratedCertSuffix)

	if !vault.IsVaultSecretBackend() {
		secretVolume := statefulset.CreateVolumeFromSecret(util.SecretVolumeName, volumeSecretName, optionalSecretFunc)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			MountPath: secretMountPath,
			Name:      secretVolume.Name,
			ReadOnly:  true,
		})
		volumes = append(volumes, secretVolume)
	}

	caVolume := statefulset.CreateVolumeFromConfigMap(tls.ConfigMapVolumeCAName, caName, optionalConfigMapFunc)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		MountPath: configmapMountPath,
		Name:      caVolume.Name,
		ReadOnly:  true,
	})
	volumes = append(volumes, caVolume)
	return volumes, volumeMounts
}

func (c *tlsVolumeSource) GetVolumes() []corev1.Volume {
	volumes, _ := c.getVolumesAndMounts()
	return volumes
}

func (c *tlsVolumeSource) GetVolumeMounts() []corev1.VolumeMount {
	_, volumeMounts := c.getVolumesAndMounts()
	return volumeMounts
}

func (c *tlsVolumeSource) GetEnvs() []corev1.EnvVar {
	return []corev1.EnvVar{}
}

func (c *tlsVolumeSource) ShouldBeAdded() bool {
	return c.security.IsTLSEnabled()
}
