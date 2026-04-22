package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
)

func TestInjectorSidecarModifications(t *testing.T) {
	cfg := &InjectorSidecarConfig{
		Image:             "monarch-injector:latest",
		ShardID:           "0",
		ReplSetName:       "standby-rs",
		ReplSetHosts:      "pod-0.svc.ns.svc.cluster.local,pod-1.svc.ns.svc.cluster.local",
		ClusterPrefix:     "failoverdemo",
		S3Bucket:          "my-bucket",
		S3Endpoint:        "http://minio:9000",
		S3PathStyleAccess: true,
		AWSRegion:         "us-east-1",
		CredentialsSecret: "monarch-s3-creds",
	}

	mods := InjectorSidecarModifications(cfg)
	require.NotEmpty(t, mods)

	c := &corev1.Container{}
	container.Apply(func(c *corev1.Container) {
		for _, m := range mods {
			m(c)
		}
	})(c)

	assert.Equal(t, "monarch-injector", c.Name)
	assert.Equal(t, "monarch-injector:latest", c.Image)

	// Verify ports.
	portNames := make(map[string]int32)
	for _, p := range c.Ports {
		portNames[p.Name] = p.ContainerPort
	}
	assert.EqualValues(t, 8080, portNames["health"])
	assert.EqualValues(t, 9995, portNames["replication"])
	assert.EqualValues(t, 1122, portNames["monarch-api"])

	// Verify env vars.
	envMap := make(map[string]corev1.EnvVar)
	for _, e := range c.Env {
		envMap[e.Name] = e
	}

	assert.Equal(t, "standby-rs", envMap["REPLSET_NAME"].Value)
	assert.Equal(t, "failoverdemo", envMap["CLUSTER_PREFIX"].Value)
	assert.Equal(t, "my-bucket", envMap["S3_BUCKET"].Value)
	assert.Equal(t, "true", envMap["S3_PATH_STYLE"].Value)
	assert.Equal(t, "9995", envMap["PORT"].Value)
	assert.Equal(t, "8080", envMap["HEALTH_PORT"].Value)
	assert.Equal(t, "1122", envMap["MONARCH_API_PORT"].Value)

	// AWS credentials must come from secretKeyRef, not inline.
	accessKeyEnv := envMap["AWS_ACCESS_KEY_ID"]
	require.NotNil(t, accessKeyEnv.ValueFrom, "AWS_ACCESS_KEY_ID must use secretKeyRef")
	assert.Equal(t, "monarch-s3-creds", accessKeyEnv.ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "awsAccessKeyId", accessKeyEnv.ValueFrom.SecretKeyRef.Key)

	secretAccessKeyEnv := envMap["AWS_SECRET_ACCESS_KEY"]
	require.NotNil(t, secretAccessKeyEnv.ValueFrom, "AWS_SECRET_ACCESS_KEY must use secretKeyRef")
	assert.Equal(t, "monarch-s3-creds", secretAccessKeyEnv.ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "awsSecretAccessKey", secretAccessKeyEnv.ValueFrom.SecretKeyRef.Key)
}
