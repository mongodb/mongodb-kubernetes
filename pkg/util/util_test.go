package util

import (
	"testing"

	"os"

	"github.com/stretchr/testify/assert"
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

func TestHash(t *testing.T) {
	s1 := &struct {
		Foo    string
		Bar    string
		Baz    int
		FooBar float32
		BarFoo float64
		Nested struct {
			NestedFoo string
			NestedBar int
		}
	}{
		"foo",
		"bar",
		1,
		float32(2.5),
		float64(5.6),
		struct {
			NestedFoo string
			NestedBar int
		}{
			"Hello",
			123,
		},
	}
	firstHash, _ := Hash(s1)
	secondHash, err := Hash(s1)
	assert.Nil(t, err, "There should not have been an error when hashing the struct.")
	assert.Equal(t, firstHash, secondHash, "First hash did not match second hash.")

	s1.Nested.NestedFoo = "different"
	postChange, _ := Hash(s1)
	assert.NotEqual(t, firstHash, postChange, "When a field is changed, it should not hash to the same value.")

	s1.Nested.NestedFoo = "Hello"
	backToOriginal, _ := Hash(s1)
	assert.Equal(t, firstHash, backToOriginal, "When changed back, it should hash to the same value.")

	s1.Foo = "FOO"
	caseShouldMatter, _ := Hash(s1)
	assert.NotEqual(t, firstHash, caseShouldMatter, "Case should matter.")
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
