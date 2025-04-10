package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetDesiredStatus(t *testing.T) {
	t.Run("Test transition from enabled to disabled", func(t *testing.T) {
		desired := Config{
			Status: Stopped,
		}
		current := Config{
			Status: Started,
		}
		assert.Equal(t, Stopped, getDesiredStatus(&desired, &current))
	})
	t.Run("Test transition from disabled to enabled", func(t *testing.T) {
		desired := Config{
			Status: Started,
		}
		current := Config{
			Status: Stopped,
		}
		assert.Equal(t, Started, getDesiredStatus(&desired, &current))
	})
	t.Run("Test transition from enabled to terminated", func(t *testing.T) {
		desired := Config{
			Status: Terminating,
		}
		current := Config{
			Status: Started,
		}
		assert.Equal(t, Stopped, getDesiredStatus(&desired, &current))
	})

	t.Run("Test transition from terminated to disabled", func(t *testing.T) {
		desired := Config{
			Status: Stopped,
		}
		current := Config{
			Status: Terminating,
		}
		assert.Equal(t, Started, getDesiredStatus(&desired, &current))
	})
}
