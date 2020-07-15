package stringutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainsAny(t *testing.T) {
	assert.True(t, ContainsAny([]string{"one", "two"}, "one"))
	assert.True(t, ContainsAny([]string{"one", "two"}, "two"))
	assert.True(t, ContainsAny([]string{"one", "two"}, "one", "two"))
	assert.True(t, ContainsAny([]string{"one", "two"}, "one", "two", "three"))

	assert.False(t, ContainsAny([]string{"one", "two"}, "three"))
	assert.False(t, ContainsAny([]string{"one", "two"}))
}
