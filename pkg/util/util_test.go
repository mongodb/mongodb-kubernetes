package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"os"
)

func TestParseMongodbMinorVersionCorrect(t *testing.T) {
	version, e := ParseMongodbMinorVersion("3.4.5")
	assert.Nil(t, e)
	assert.Equal(t, float32(3.4), version)

	// we don't perform detailed validation of data (so numbers can be any)
	version, e = ParseMongodbMinorVersion("3.48.55")
	assert.Nil(t, e)
	assert.Equal(t, float32(3.48), version)

	version, e = ParseMongodbMinorVersion("-5.0.0")
	assert.Nil(t, e)
	assert.Equal(t, float32(-5.0), version)

	version, e = ParseMongodbMinorVersion("4.0")
	assert.Nil(t, e)
	assert.Equal(t, float32(4.0), version)

	version, e = ParseMongodbMinorVersion("20.30")
	assert.Nil(t, e)
	assert.Equal(t, float32(20.30), version)

	// any incorrect symbols after first two numbers are fine
	version, e = ParseMongodbMinorVersion("4.3.l")
	assert.Nil(t, e)
	assert.Equal(t, float32(4.3), version)

}

func TestParseMongodbMinorVersionWrong(t *testing.T) {
	_, e := ParseMongodbMinorVersion("1.")
	assert.NotNil(t, e)

	_, e = ParseMongodbMinorVersion("10")
	assert.NotNil(t, e)

	_, e = ParseMongodbMinorVersion("3.5.8.10")
	assert.NotNil(t, e)

	_, e = ParseMongodbMinorVersion("4.a.8")
	assert.NotNil(t, e)

	_, e = ParseMongodbMinorVersion("a.9.8")
	assert.NotNil(t, e)

	_, e = ParseMongodbMinorVersion("5.@")
	assert.NotNil(t, e)

}

func TestReadBoolEnv(t *testing.T) {
	os.Setenv("ENV_1", "true")
	os.Setenv("ENV_2", "false")
	os.Setenv("ENV_3", "TRUE")
	os.Setenv("NOT_BOOL", "not-true")

	result, present := ReadBoolEnv("ENV_1")
	assert.True(t, present)
	assert.True(t, result)

	result, present = ReadBoolEnv("ENV_2")
	assert.True(t, present)
	assert.False(t, result)

	result, present = ReadBoolEnv("ENV_3")
	assert.True(t, present)
	assert.True(t, result)

	result, present = ReadBoolEnv("NOT_BOOL")
	assert.False(t, present)
	assert.False(t, result)

	result, present = ReadBoolEnv("NOT_HERE")
	assert.False(t, present)
	assert.False(t, result)
}
