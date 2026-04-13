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

func TestRemoveFieldsBasedOnDesiredAndPrevious(t *testing.T) {
	p := map[string]interface{}{
		"one": "oneValue",
		"two": map[string]interface{}{
			"three": "threeValue",
			"four":  "fourValue",
		},
	}

	// we are removing the "two.three" entry in this case.
	spec := map[string]interface{}{
		"one": "oneValue",
		"two": map[string]interface{}{
			"four": "fourValue",
		},
	}

	prev := map[string]interface{}{
		"one": "oneValue",
		"two": map[string]interface{}{
			"three": "threeValue",
			"four":  "fourValue",
		},
	}

	expected := map[string]interface{}{
		"one": "oneValue",
		"two": map[string]interface{}{
			"four": "fourValue",
		},
	}

	actual := RemoveFieldsBasedOnDesiredAndPrevious(p, spec, prev)
	assert.Equal(t, expected, actual, "three was set previously, and so should have been removed.")
}
