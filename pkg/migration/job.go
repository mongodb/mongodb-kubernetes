package migration

import (
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// JobConfig holds what the operator knows at Job-creation time.
type JobConfig struct {
	Name      string
	Namespace string
	// OperatorImage is the operator's own image ref; the connectivity-validator binary
	// is compiled into the same image so no separate image is needed.
	OperatorImage    string
	ConnectionString string
	ExternalMembers  []string // TODO: populated from spec.externalMembers once that PR lands
	AuthMechanism    string
	// KeyfileSecretRef is the Secret name containing the keyfile (SCRAM) or cert (X509).
	KeyfileSecretRef string
}

const (
	keyfileSecretMountPath = "/var/run/credentials/keyfile"
	certSecretMountPath    = "/var/run/credentials/cert.pem"
	caSecretMountPath      = "/var/run/credentials/ca.pem"

	keyfileSecretKey = "keyfile"
	certSecretKey    = "cert.pem"
	caSecretKey      = "ca.pem"
)

// BuildJob returns a batch/v1 Job spec for the connectivity validator.
func BuildJob(cfg JobConfig) *batchv1.Job {
	backoffLimit := int32(0)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name + "-connectivity-check",
			Namespace: cfg.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:         "connectivity-validator",
						Image:        cfg.OperatorImage,
						Command:      []string{"/usr/local/bin/connectivity-validator"},
						Env:          buildEnvVars(cfg),
						VolumeMounts: buildVolumeMounts(cfg),
					}},
					Volumes: buildVolumes(cfg),
				},
			},
		},
	}
}

func buildEnvVars(cfg JobConfig) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{Name: "CONNECTION_STRING", Value: cfg.ConnectionString},
		{Name: "AUTH_MECHANISM", Value: cfg.AuthMechanism},
		{Name: "EXTERNAL_MEMBERS", Value: strings.Join(cfg.ExternalMembers, " ")},
	}
	switch cfg.AuthMechanism {
	case "SCRAM-SHA-256":
		envVars = append(envVars, corev1.EnvVar{Name: "KEYFILE_PATH", Value: keyfileSecretMountPath})
	case "MONGODB-X509":
		envVars = append(envVars,
			corev1.EnvVar{Name: "CERT_PATH", Value: certSecretMountPath},
			corev1.EnvVar{Name: "CA_PATH", Value: caSecretMountPath},
		)
	}
	return envVars
}

func buildVolumeMounts(cfg JobConfig) []corev1.VolumeMount {
	if cfg.KeyfileSecretRef == "" {
		return nil
	}
	return []corev1.VolumeMount{{
		Name:      "credentials",
		MountPath: "/var/run/credentials",
		ReadOnly:  true,
	}}
}

func buildVolumes(cfg JobConfig) []corev1.Volume {
	if cfg.KeyfileSecretRef == "" {
		return nil
	}
	return []corev1.Volume{{
		Name: "credentials",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: cfg.KeyfileSecretRef,
			},
		},
	}}
}
