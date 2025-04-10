package env

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
