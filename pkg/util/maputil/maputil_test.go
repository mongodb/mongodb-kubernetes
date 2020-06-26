package maputil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetMapValue(t *testing.T) {
	t.Run("Set to empty map", func(t *testing.T) {
		dest := map[string]interface{}{}
		SetMapValue(dest, 30, "one", "two", "three")
		expectedMap := map[string]interface{}{
			"one": map[string]interface{}{
				"two": map[string]interface{}{
					"three": 30,
				},
			},
		}
		assert.Equal(t, expectedMap, dest)
	})
	t.Run("Set to non-empty map", func(t *testing.T) {
		dest := map[string]interface{}{
			"one": map[string]interface{}{
				"ten": "bar",
				"two": map[string]interface{}{
					"three":  100,
					"eleven": true,
				},
			},
		}
		SetMapValue(dest, 30, "one", "two", "three")
		expectedMap := map[string]interface{}{
			"one": map[string]interface{}{
				"ten": "bar",
				"two": map[string]interface{}{
					"three":  30, // this was changed
					"eleven": true,
				},
			},
		}
		assert.Equal(t, expectedMap, dest)
	})

}
