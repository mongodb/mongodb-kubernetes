package migration

import (
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// Labels for connectivity validation Jobs (used by both Job build and jobrunner).
const (
	ConnectivityCheckReplicaSetLabel = "mongodb.k8s.io/connectivity-check-replica-set"
	ConnectivityCheckDryRunLabel     = "mongodb.k8s.io/connectivity-check-dry-run"
	OperatorManagedByLabel           = "app.kubernetes.io/managed-by"
	OperatorManagedByValue           = "mongodb-kubernetes-operator"

	// DefaultTTLSecondsAfterFinished is how long after completion (success or failure)
	// Kubernetes will keep the Job and its Pods before auto-deleting them.
	DefaultTTLSecondsAfterFinished = 600 // 10 minutes
)

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

// volumesAndMountsFromStatefulSet returns volumes and volume mounts from the StatefulSet pod
// template, excluding any volume that uses a PersistentVolumeClaim (e.g. data, logs). The Job
// cannot use PVCs, so we only copy volumes that are backed by Secret, ConfigMap, etc.
func volumesAndMountsFromStatefulSet(sts *appsv1.StatefulSet) ([]corev1.Volume, []corev1.VolumeMount) {
	var vols []corev1.Volume
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			continue
		}
		vols = append(vols, v)
	}
	volumeNames := make(map[string]struct{})
	for i := range vols {
		volumeNames[vols[i].Name] = struct{}{}
	}
	var mounts []corev1.VolumeMount
	if len(sts.Spec.Template.Spec.Containers) > 0 {
		for _, m := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
			if _, ok := volumeNames[m.Name]; ok {
				mounts = append(mounts, m)
			}
		}
	}
	return vols, mounts
}

// BuildJobFromStatefulSet builds a connectivity validation Job that uses the same credentials
// volumes and mounts as the given StatefulSet, so STS and Job share the same code path.
// agentCertHash is the hash key of the agent cert PEM file (path becomes AgentCertMountPath/hash).
// subjectDN is the automation agent X.509 subject (RFC 4514) for MONGODB-X509; empty for SCRAM.
func BuildJobFromStatefulSet(rs *mdbv1.MongoDB, sts *appsv1.StatefulSet, operatorImage, connectionString string, externalMembers []string, currentAgentAuthMode, agentCertHash, subjectDN string) *batchv1.Job {
	volumes, volumeMounts := volumesAndMountsFromStatefulSet(sts)
	security := rs.GetSecurity()
	automationAuthEnabled := security != nil && security.Authentication != nil && security.Authentication.Enabled
	currentAgentMechanism := security.GetAgentMechanism(currentAgentAuthMode)
	var authMechanism string
	if currentAgentMechanism != "" {
		m := authentication.ConvertToMechanismOrPanic(currentAgentMechanism, currentAgentAuthMode, automationAuthEnabled)
		authMechanism = string(m.GetName())
	}

	keyfilePath := util.InternalClusterAuthMountPath + "keyfile"
	certPath := util.AgentCertMountPath + "/" + agentCertHash

	var caPath string
	switch authMechanism {
	case util.AutomationConfigX509Option:
		// For X509/TLS, the CA is mounted at TLSCaMountPath with the file named "ca-pem"
		caPath = util.TLSCaMountPath + "/ca-pem"
	default:
		caPath = util.CAFilePathInContainer
	}

	envVars := []corev1.EnvVar{
		{Name: "CONNECTION_STRING", Value: connectionString},
		{Name: "AUTH_MECHANISM", Value: authMechanism},
		{Name: "EXTERNAL_MEMBERS", Value: strings.Join(externalMembers, " ")},
		{Name: "KEYFILE_PATH", Value: keyfilePath},
		{Name: "CERT_PATH", Value: certPath},
		{Name: "CA_PATH", Value: caPath},
		{Name: "SUBJECT_DN", Value: subjectDN},
	}

	backoffLimit := int32(0)
	ttlSecondsAfterFinished := int32(DefaultTTLSecondsAfterFinished)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rs.Name + "-connectivity-check",
			Namespace: rs.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
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

	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
	job.Labels[ConnectivityCheckReplicaSetLabel] = rs.Name
	job.Labels[ConnectivityCheckDryRunLabel] = "true"
	job.Labels[OperatorManagedByLabel] = OperatorManagedByValue

	job.OwnerReferences = kube.BaseOwnerReference(rs)

	return job
}
