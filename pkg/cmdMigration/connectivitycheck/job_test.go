package connectivitycheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
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
	assert.Equal(t, keyfileSecretMountPath, findEnv(container.Env, "KEYFILE_PATH"))
	assert.Equal(t, "", findEnv(container.Env, "CERT_PATH"))
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
	assert.Equal(t, certSecretMountPath, findEnv(container.Env, "CERT_PATH"))
	assert.Equal(t, caSecretMountPath, findEnv(container.Env, "CA_PATH"))
	assert.Equal(t, "", findEnv(container.Env, "KEYFILE_PATH"))
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

func TestBuildJob_VolumeMount(t *testing.T) {
	job := BuildJob(JobConfig{
		Name:             "my-rs",
		Namespace:        "default",
		OperatorImage:    "quay.io/mongodb/mongodb-kubernetes-operator:1.0",
		AuthMechanism:    "SCRAM-SHA-256",
		KeyfileSecretRef: "my-rs-keyfile",
	})

	container := job.Spec.Template.Spec.Containers[0]
	assert.Len(t, container.VolumeMounts, 1)
	assert.Equal(t, "/var/run/credentials", container.VolumeMounts[0].MountPath)
	assert.True(t, container.VolumeMounts[0].ReadOnly)

	assert.Len(t, job.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, "my-rs-keyfile", job.Spec.Template.Spec.Volumes[0].VolumeSource.Secret.SecretName)
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
