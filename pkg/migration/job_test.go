package migration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestBuildJob_BasicShape(t *testing.T) {
	job := BuildJob(JobConfig{
		Name:             "my-rs",
		Namespace:        "default",
		OperatorImage:    "quay.io/mongodb/mongodb-kubernetes-operator:1.0",
		ConnectionString: "mongodb://rs0/host1:27017,host2:27017",
		ExternalMembers:  []string{"host1:27017", "host2:27017"},
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfileSecretRef: "my-rs-keyfile",
	})

	assert.Equal(t, "my-rs-connectivity-check", job.Name)
	assert.Equal(t, "default", job.Namespace)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)

	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "connectivity-validator", container.Name)
	assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-operator:1.0", container.Image)
	assert.Equal(t, []string{"/usr/local/bin/connectivity-validator"}, container.Command)
}

func TestBuildJob_SCRAMEnvVars(t *testing.T) {
	job := BuildJob(JobConfig{
		Name:             "my-rs",
		Namespace:        "default",
		OperatorImage:    "quay.io/mongodb/mongodb-kubernetes-operator:1.0",
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfileSecretRef: "my-rs-keyfile",
	})

	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "SCRAM-SHA-256", findEnv(container.Env, "AUTH_MECHANISM"))
	// Credential paths come from STS in BuildJobFromStatefulSet; BuildJob does not set them
	assert.Equal(t, "", findEnv(container.Env, "KEYFILE_PATH"))
}

func TestBuildJob_X509EnvVars(t *testing.T) {
	job := BuildJob(JobConfig{
		Name:             "my-rs",
		Namespace:        "default",
		OperatorImage:    "quay.io/mongodb/mongodb-kubernetes-operator:1.0",
		AuthMechanism:    "MONGODB-X509",
		KeyfileSecretRef: "my-rs-cert",
	})

	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "MONGODB-X509", findEnv(container.Env, "AUTH_MECHANISM"))
	// Credential paths come from STS in BuildJobFromStatefulSet; BuildJob does not set them
	assert.Equal(t, "", findEnv(container.Env, "CERT_PATH"))
	assert.Equal(t, "", findEnv(container.Env, "CA_PATH"))
}

func TestBuildJob_ExternalMembersJoined(t *testing.T) {
	job := BuildJob(JobConfig{
		Name:            "my-rs",
		Namespace:       "default",
		OperatorImage:   "quay.io/mongodb/mongodb-kubernetes-operator:1.0",
		AuthMechanism:   "SCRAM-SHA-256",
		ExternalMembers: []string{"host1:27017", "host2:27017", "host3:27017"},
	})

	container := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "host1:27017 host2:27017 host3:27017", findEnv(container.Env, "EXTERNAL_MEMBERS"))
}

// TestBuildJobFromStatefulSet_IncludesCredentials asserts the Job gets credential volumes from the STS.
func TestBuildJobFromStatefulSet_IncludesCredentials(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{{
						Name: util.ClusterFileName,
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "my-rs-clusterfile"},
						},
					}},
					Containers: []corev1.Container{{
						VolumeMounts: []corev1.VolumeMount{{
							Name:      util.ClusterFileName,
							MountPath: "/var/run/credentials",
							ReadOnly:  true,
						}},
					}},
				},
			},
		},
	}
	rs := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "my-rs", Namespace: "default"}}
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	assert.NotEmpty(t, job.Spec.Template.Spec.Volumes)
	assert.NotEmpty(t, job.Spec.Template.Spec.Containers[0].VolumeMounts)
}

func TestBuildJob_NoSecret(t *testing.T) {
	job := BuildJob(JobConfig{
		Name:          "my-rs",
		Namespace:     "default",
		OperatorImage: "quay.io/mongodb/mongodb-kubernetes-operator:1.0",
		AuthMechanism: "SCRAM-SHA-256",
	})
	container := job.Spec.Template.Spec.Containers[0]
	assert.Empty(t, container.VolumeMounts)
	assert.Empty(t, job.Spec.Template.Spec.Volumes)
}

// findEnv returns the value of the env var with the given name, or "" if not found.
func findEnv(envVars []corev1.EnvVar, name string) string {
	for _, e := range envVars {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// newTestRS builds a minimal MongoDB CR for use in BuildJobConfigFromRS tests.
func newTestRS(name, namespace string, security *mdbv1.Security) *mdbv1.MongoDB {
	rs := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Security: security,
			},
		},
	}
	rs.Spec.ResourceType = mdbv1.ReplicaSet
	return rs
}
