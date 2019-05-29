package util

import (
	"testing"

	"os"

	"github.com/stretchr/testify/assert"
)

func TestCompareVersions(t *testing.T) {
	i, e := CompareVersions("4.0.5", "4.0.4")
	assert.NoError(t, e)
	assert.Equal(t, 1, i)

	i, e = CompareVersions("4.0.0", "4.0.0")
	assert.NoError(t, e)
	assert.Equal(t, 0, i)

	i, e = CompareVersions("3.6.15", "4.1.0")
	assert.NoError(t, e)
	assert.Equal(t, -1, i)

	i, e = CompareVersions("3.6.2", "3.6.12")
	assert.NoError(t, e)
	assert.Equal(t, -1, i)

	i, e = CompareVersions("4.0.2-ent", "4.0.1")
	assert.NoError(t, e)
	assert.Equal(t, 1, i)
}

func TestMajorMinorVersion(t *testing.T) {
	s, e := MajorMinorVersion("3.6.12")
	assert.NoError(t, e)
	assert.Equal(t, "3.6", s)

	s, e = MajorMinorVersion("4.0.0")
	assert.NoError(t, e)
	assert.Equal(t, "4.0", s)

	s, e = MajorMinorVersion("4.2.12-ent")
	assert.NoError(t, e)
	assert.Equal(t, "4.2", s)
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
