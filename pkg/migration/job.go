package migration

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// credentialVolumeNames are the volume names used by the STS for agent/internal cluster auth.
// The Job reuses the same volumes so the connectivity validator sees the same mounts.
var credentialVolumeNames = map[string]struct{}{
	util.AgentSecretName: {},
	util.ClusterFileName: {},
}

// JobConfig holds what the operator knows at Job-creation time.
type JobConfig struct {
	Name      string
	Namespace string
	// OperatorImage is the operator's own image ref; the connectivity-validator binary
	// is compiled into the same image so no separate image is needed.
	OperatorImage    string
	ConnectionString string
	ExternalMembers  []string
	AuthMechanism    string
	// KeyfileSecretRef is the Secret name containing the keyfile (SCRAM) or cert (X509).
	KeyfileSecretRef string
}

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
						Name:            "connectivity-validator",
						Image:           cfg.OperatorImage,
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"/usr/local/bin/connectivity-validator"},
						Env:             buildEnvVars(cfg),
					}},
				},
			},
		},
	}
}

func buildEnvVars(cfg JobConfig) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "CONNECTION_STRING", Value: cfg.ConnectionString},
		{Name: "AUTH_MECHANISM", Value: cfg.AuthMechanism},
		{Name: "EXTERNAL_MEMBERS", Value: strings.Join(cfg.ExternalMembers, " ")},
	}
}

// credentialsVolumesAndMountsFromStatefulSet returns the volumes and volume mounts from the
// StatefulSet pod template that are used for credentials (agent cert, internal cluster auth).
// The Job uses the same so the connectivity validator sees the same paths as the database pods.
func credentialsVolumesAndMountsFromStatefulSet(sts *appsv1.StatefulSet) ([]corev1.Volume, []corev1.VolumeMount) {
	var vols []corev1.Volume
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if _, ok := credentialVolumeNames[v.Name]; ok {
			vols = append(vols, v)
		}
	}
	var mounts []corev1.VolumeMount
	if len(sts.Spec.Template.Spec.Containers) > 0 {
		for _, m := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
			if _, ok := credentialVolumeNames[m.Name]; ok {
				mounts = append(mounts, m)
			}
		}
	}
	return vols, mounts
}

// BuildJobFromStatefulSet builds a connectivity validation Job that uses the same credentials
// volumes and mounts as the given StatefulSet, so STS and Job share the same code path.
// agentCertHash is the hash key of the agent cert PEM file (path becomes AgentCertMountPath/hash).
// internalClusterCertPath is the full path used for internal cluster auth (e.g. InternalClusterAuthMountPath+hash for X509).
func BuildJobFromStatefulSet(rs *mdbv1.MongoDB, sts *appsv1.StatefulSet, operatorImage, connectionString string, externalMembers []string, currentAgentAuthMode, agentCertHash, internalClusterCertPath string) *batchv1.Job {
	volumes, volumeMounts := credentialsVolumesAndMountsFromStatefulSet(sts)
	security := rs.GetSecurity()
	agentMechanism := security.GetAgentMechanism(currentAgentAuthMode)

	keyfilePath := util.InternalClusterAuthMountPath + "keyfile"
	certPath := util.AgentCertMountPath + "/" + agentCertHash

	var authMechanism string
	switch agentMechanism {
	case util.X509:
		authMechanism = util.AutomationConfigX509Option
	case util.SCRAM, util.SCRAMSHA256:
		authMechanism = util.AutomationConfigScramSha256Option
	default:
		authMechanism = ""
	}

	envVars := []corev1.EnvVar{
		{Name: "CONNECTION_STRING", Value: connectionString},
		{Name: "AUTH_MECHANISM", Value: authMechanism},
		{Name: "EXTERNAL_MEMBERS", Value: strings.Join(externalMembers, " ")},
		{Name: "KEYFILE_PATH", Value: keyfilePath},
		{Name: "CERT_PATH", Value: certPath},
		{Name: "CA_PATH", Value: certPath},
	}

	backoffLimit := int32(0)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rs.Name + "-connectivity-check",
			Namespace: rs.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:            "connectivity-validator",
						Image:           operatorImage,
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"/usr/local/bin/connectivity-validator"},
						Env:             envVars,
						VolumeMounts:    volumeMounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}
