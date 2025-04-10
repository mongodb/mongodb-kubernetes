package int

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIntMin(t *testing.T) {
	var min int

	min = Min(1, 2)
	assert.Equal(t, 1, min)

	min = Min(-1, -2)
	assert.Equal(t, -2, min)

	min = Min(-2, 10)
	assert.Equal(t, -2, min)
}
