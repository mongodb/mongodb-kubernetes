package migration

import (
	"fmt"
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

	var authMechanism string
	var envVars []corev1.EnvVar
	switch agentMechanism {
	case util.X509:
		authMechanism = util.AutomationConfigX509Option
		certPath := util.AgentCertMountPath + "/" + agentCertHash
		envVars = append(envVars,
			corev1.EnvVar{Name: "CONNECTION_STRING", Value: connectionString},
			corev1.EnvVar{Name: "AUTH_MECHANISM", Value: authMechanism},
			corev1.EnvVar{Name: "EXTERNAL_MEMBERS", Value: strings.Join(externalMembers, " ")}, // TODO: most likely we should get this from the secret
			corev1.EnvVar{Name: "CERT_PATH", Value: certPath},
			corev1.EnvVar{Name: "CA_PATH", Value: certPath},
		)
	case util.SCRAM, util.SCRAMSHA256:
		authMechanism = util.AutomationConfigScramSha256Option
		keyfilePath := util.InternalClusterAuthMountPath + "keyfile"
		envVars = append(envVars,
			corev1.EnvVar{Name: "CONNECTION_STRING", Value: connectionString},
			corev1.EnvVar{Name: "AUTH_MECHANISM", Value: authMechanism},
			corev1.EnvVar{Name: "EXTERNAL_MEMBERS", Value: strings.Join(externalMembers, " ")},
			corev1.EnvVar{Name: "KEYFILE_PATH", Value: keyfilePath},
		)
	default:
		authMechanism = ""
		envVars = append(envVars,
			corev1.EnvVar{Name: "CONNECTION_STRING", Value: connectionString},
			corev1.EnvVar{Name: "AUTH_MECHANISM", Value: authMechanism},
			corev1.EnvVar{Name: "EXTERNAL_MEMBERS", Value: strings.Join(externalMembers, " ")},
		)
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
						Name:         "connectivity-validator",
						Image:        operatorImage,
						Command:      []string{"/usr/local/bin/connectivity-validator"},
						Env:          envVars,
						VolumeMounts: volumeMounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// BuildJobConfigFromRS derives a JobConfig from a MongoDB ReplicaSet CR.
//
// externalMembers is the list of host:port pairs from the current Ops Manager deployment
// (obtained via conn.ReadDeployment().GetAllHostnames()) that the validator should reach.
//
// operatorImage is the full image reference of the running operator pod (from util.OperatorImageEnv).
//
// currentAgentAuthMode is the agent authentication mode returned by conn.GetAgentAuthMode().
func BuildJobConfigFromRS(rs *mdbv1.MongoDB, operatorImage, currentAgentAuthMode string, externalMembers []string) JobConfig {
	security := rs.GetSecurity()
	agentMechanism := security.GetAgentMechanism(currentAgentAuthMode)

	var authMechanism, keyfileSecretRef string
	switch agentMechanism {
	case util.X509:
		authMechanism = util.AutomationConfigX509Option
		keyfileSecretRef = security.AgentClientCertificateSecretName(rs.Name)
	case util.SCRAM, util.SCRAMSHA256:
		authMechanism = util.AutomationConfigScramSha256Option
		keyfileSecretRef = security.InternalClusterAuthSecretName(rs.Name)
	default:
		// Auth disabled or no mechanism: no credentials secret needed.
		authMechanism = ""
		keyfileSecretRef = ""
	}

	connectionString := fmt.Sprintf("mongodb://%s/?replicaSet=%s", strings.Join(externalMembers, ","), rs.Name)

	return JobConfig{
		Name:             rs.Name,
		Namespace:        rs.Namespace,
		OperatorImage:    operatorImage,
		ConnectionString: connectionString,
		ExternalMembers:  externalMembers,
		AuthMechanism:    authMechanism,
		KeyfileSecretRef: keyfileSecretRef,
	}
}
