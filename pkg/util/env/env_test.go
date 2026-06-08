package env

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
)

func TestReadBoolEnv(t *testing.T) {
	t.Setenv("ENV_1", "true")
	t.Setenv("ENV_2", "false")
	t.Setenv("ENV_3", "TRUE")
	t.Setenv("NOT_BOOL", "not-true")

	result, present := ReadBool("ENV_1")
	assert.True(t, present)
	assert.True(t, result)

	result, present = ReadBool("ENV_2")
	assert.True(t, present)
	assert.False(t, result)

	result, present = ReadBool("ENV_3")
	assert.True(t, present)
	assert.True(t, result)

	result, present = ReadBool("NOT_BOOL")
	assert.False(t, present)
	assert.False(t, result)

	result, present = ReadBool("NOT_HERE")
	assert.False(t, present)
	assert.False(t, result)
}

func TestMergeWithOverride(t *testing.T) {
	existing := []corev1.EnvVar{
		{
			Name:  "C_env",
			Value: "C_value",
		},
		{
			Name:  "B_env",
			Value: "B_value",
		},
		{
			Name:  "A_env",
			Value: "A_value",
		},
		{
			Name: "F_env",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key: "f_key",
				},
			},
		},
	}

	desired := []corev1.EnvVar{
		{
			Name:  "D_env",
			Value: "D_value",
		},
		{
			Name:  "E_env",
			Value: "E_value",
		},
		{
			Name:  "C_env",
			Value: "C_value_new",
		},
		{
			Name:  "B_env",
			Value: "B_value_new",
		},
		{
			Name:  "A_env",
			Value: "A_value",
		},
	}

	merged := MergeWithOverride(existing, desired)

	t.Run("EnvVars should be sorted", func(t *testing.T) {
		assert.Equal(t, "A_env", merged[0].Name)
		assert.Equal(t, "B_env", merged[1].Name)
		assert.Equal(t, "C_env", merged[2].Name)
		assert.Equal(t, "D_env", merged[3].Name)
		assert.Equal(t, "E_env", merged[4].Name)
		assert.Equal(t, "F_env", merged[5].Name)
	})

	t.Run("EnvVars of same name are updated", func(t *testing.T) {
		assert.Equal(t, "B_env", merged[1].Name)
		assert.Equal(t, "B_value_new", merged[1].Value)
	})

	t.Run("Existing EnvVars are not touched", func(t *testing.T) {
		envVar := merged[5]
		assert.NotNil(t, envVar.ValueFrom)
		assert.Equal(t, "f_key", envVar.ValueFrom.SecretKeyRef.Key)
	})
}
