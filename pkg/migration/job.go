package migration

import (
	"path"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// Labels for connectivity validation Jobs (used by both Job build and jobrunner).
const (
	ConnectivityCheckReplicaSetLabel = "mongodb.k8s.io/connectivity-check-replica-set"
	ConnectivityCheckDryRunLabel     = "mongodb.k8s.io/connectivity-check-dry-run"
	OperatorManagedByLabel           = "app.kubernetes.io/managed-by"
	OperatorManagedByValue           = "mongodb-kubernetes-operator"

	// ConnectivityValidatorContainerName is the Job pod container name and basename of the binary under /usr/local/bin/.
	ConnectivityValidatorContainerName = "connectivity-validator"

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
}

// nonPVCVolumes returns pod-template volumes that are not backed by a PersistentVolumeClaim,
// and a set of their names for filtering mounts. Jobs cannot use PVCs. Volumes whose name is in
// excluded are dropped as well, see volumesAndMountsFromStatefulSet.
func nonPVCVolumes(sts *appsv1.StatefulSet, excluded map[string]struct{}) ([]corev1.Volume, map[string]struct{}) {
	var vols []corev1.Volume
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			continue
		}
		if _, ok := excluded[v.Name]; ok {
			continue
		}
		vols = append(vols, v)
	}
	names := make(map[string]struct{}, len(vols))
	for i := range vols {
		names[vols[i].Name] = struct{}{}
	}
	return vols, names
}

// volumeMountIdentity holds the fields that identify a mount for deduplication.
// corev1.VolumeMount cannot be a map key because it contains pointer fields.
type volumeMountIdentity struct {
	name, mountPath, subPath string
}

func identityOfVolumeMount(m corev1.VolumeMount) volumeMountIdentity {
	return volumeMountIdentity{name: m.Name, mountPath: m.MountPath, subPath: m.SubPath}
}

// dedupedVolumeMounts collects volume mounts from every container that reference an allowed
// volume name, deduplicated by (name, mountPath, subPath) so static multi-container pods do
// not produce duplicate mounts on the Job's single container.
func dedupedVolumeMounts(containers []corev1.Container, allowedVolumeNames map[string]struct{}) []corev1.VolumeMount {
	seen := make(map[volumeMountIdentity]struct{})
	var mounts []corev1.VolumeMount
	for i := range containers {
		for _, m := range containers[i].VolumeMounts {
			if _, ok := allowedVolumeNames[m.Name]; !ok {
				continue
			}
			id := identityOfVolumeMount(m)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			mounts = append(mounts, m)
		}
	}
	return mounts
}

// volumesAndMountsFromStatefulSet returns volumes and volume mounts from the StatefulSet pod
// template, excluding any volume that uses a PersistentVolumeClaim (e.g. data, logs) and any
// volume named in excluded. Mounts are taken from all app containers, deduplicated; init
// containers are ignored. Excluding a volume drops its mounts too, since mounts are filtered
// against the surviving volume names.
func volumesAndMountsFromStatefulSet(sts *appsv1.StatefulSet, excluded map[string]struct{}) ([]corev1.Volume, []corev1.VolumeMount) {
	vols, allowed := nonPVCVolumes(sts, excluded)
	mounts := dedupedVolumeMounts(sts.Spec.Template.Spec.Containers, allowed)
	return vols, mounts
}

// BuildJobFromStatefulSet builds a connectivity validation Job that uses the same credentials
// volumes and mounts as the given StatefulSet, so STS and Job share the same code path.
// agentCertHash is the hash key of the agent cert PEM file (path becomes AgentCertMountPath/hash).
// subjectDN is the automation agent X.509 subject (RFC 4514) for MONGODB-X509; empty for SCRAM.
func BuildJobFromStatefulSet(rs *mdbv1.MongoDB, sts *appsv1.StatefulSet, operatorImage, connectionString string, externalMembers []string, currentAgentAuthMode, agentCertHash, subjectDN string) *batchv1.Job {
	// The hostname-override ConfigMap is excluded on purpose. The validator takes its hostnames
	// from the CONNECTION_STRING and EXTERNAL_MEMBERS env vars and never reads /opt/scripts/config,
	// and inheriting the mount breaks the non-static architecture: there database-scripts is mounted
	// read-only, and only the StatefulSet's init container creates the nested /opt/scripts/config
	// mountpoint. A Job has no init container, so the container fails to start with exit code 128.
	volumes, volumeMounts := volumesAndMountsFromStatefulSet(sts, map[string]struct{}{
		rs.GetHostNameOverrideConfigmapName(): {},
	})

	security := rs.GetSecurity()
	automationAuthEnabled := security != nil && security.Authentication != nil && security.Authentication.Enabled
	currentAgentMechanism := security.GetAgentMechanism(currentAgentAuthMode)
	var authMechanism string
	if currentAgentMechanism != "" {
		m := authentication.ConvertToMechanismOrPanic(currentAgentMechanism, currentAgentAuthMode, automationAuthEnabled)
		authMechanism = string(m.GetName())
	}

	// Use autoPEMKeyFilePath from spec if set (custom mount path for migration), else default path.
	certPath := security.GetAgentAutoPEMKeyFilePath()
	if certPath == "" {
		certPath = util.AgentCertMountPath + "/" + agentCertHash
	}

	mongodTLSCAPath := ""
	if security.IsTLSEnabled() {
		// Honor a custom spec.security.tls.caFilePath: the StatefulSet-derived volume
		// mounts the CA there, so the validator must read it from the same path.
		// X.509 auth always implies TLS, so this is also the CA used for X.509.
		mongodTLSCAPath = security.GetTLSCAFilePath(path.Join(util.TLSCaMountPath, tls.CAConfigMapKey))
	}

	clientCertRequired := "false"
	if security.ShouldUseClientCertificates() {
		clientCertRequired = "true"
	}

	envVars := []corev1.EnvVar{
		{Name: "CONNECTION_STRING", Value: connectionString},
		{Name: "AUTH_MECHANISM", Value: authMechanism},
		{Name: "EXTERNAL_MEMBERS", Value: strings.Join(externalMembers, " ")},
		{Name: "CERT_PATH", Value: certPath},
		{Name: "SUBJECT_DN", Value: subjectDN},
		{Name: "MONGOD_TLS_CA_PATH", Value: mongodTLSCAPath},
		{Name: "CLIENT_CERT_REQUIRED", Value: clientCertRequired},
	}

	// Same defaults the operator applies to its StatefulSet pods, so the Job is admitted
	// under the same Pod Security Admission level. Skipped when the platform manages the
	// security context itself (OpenShift), matching podtemplatespec.WithDefaultSecurityContextsModifications.
	var podSecurityContext *corev1.PodSecurityContext
	var containerSecurityContext *corev1.SecurityContext
	if !env.ReadBoolOrDefault(podtemplatespec.ManagedSecurityContextEnv, false) { // nolint:forbidigo
		psc := podtemplatespec.DefaultPodSecurityContext()
		podSecurityContext = &psc
		csc := container.DefaultSecurityContext()
		containerSecurityContext = &csc
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
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: podSecurityContext,
					Containers: []corev1.Container{{
						Name:            ConnectivityValidatorContainerName,
						Image:           operatorImage,
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"/usr/local/bin/" + ConnectivityValidatorContainerName},
						Env:             envVars,
						VolumeMounts:    volumeMounts,
						SecurityContext: containerSecurityContext,
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
