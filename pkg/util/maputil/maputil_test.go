package maputil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetMapValue(t *testing.T) {
	t.Run("Set to empty map", func(t *testing.T) {
		dest := map[string]any{}
		SetMapValue(dest, 30, "one", "two", "three")
		expectedMap := map[string]any{
			"one": map[string]any{
				"two": map[string]any{
					"three": 30,
				},
			},
		}
		assert.Equal(t, expectedMap, dest)
	})
	t.Run("Set to non-empty map", func(t *testing.T) {
		dest := map[string]any{
			"one": map[string]any{
				"ten": "bar",
				"two": map[string]any{
					"three":  100,
					"eleven": true,
				},
			},
		}
		SetMapValue(dest, 30, "one", "two", "three")
		expectedMap := map[string]any{
			"one": map[string]any{
				"ten": "bar",
				"two": map[string]any{
					"three":  30, // this was changed
					"eleven": true,
				},
			},
		}
		assert.Equal(t, expectedMap, dest)
	})
}

func TestRemoveFieldsBasedOnDesiredAndPrevious(t *testing.T) {
	p := map[string]any{
		"one": "oneValue",
		"two": map[string]any{
			"three": "threeValue",
			"four":  "fourValue",
		},
	}

	// we are removing the "two.three" entry in this case.
	spec := map[string]any{
		"one": "oneValue",
		"two": map[string]any{
			"four": "fourValue",
		},
	}

	prev := map[string]any{
		"one": "oneValue",
		"two": map[string]any{
			"three": "threeValue",
			"four":  "fourValue",
		},
	}

	expected := map[string]any{
		"one": "oneValue",
		"two": map[string]any{
			"four": "fourValue",
		},
	}

	actual := RemoveFieldsBasedOnDesiredAndPrevious(p, spec, prev)
	assert.Equal(t, expected, actual, "three was set previously, and so should have been removed.")
}

func TestRemoveFieldsBasedOnDesiredAndPrevious_NilValueMeansRemove(t *testing.T) {
	// Simulates setting additionalMongodConfig.systemLog.verbosity to null in the CR.
	// Kubernetes preserves the null in the stored spec, so the desired map has verbosity: nil.
	// nil must be treated as "absent" so that RemoveFieldsBasedOnDesiredAndPrevious removes it.
	current := map[string]any{
		"systemLog": map[string]any{
			"verbosity": 4,
			"logAppend": true,
		},
	}
	desired := map[string]any{
		"systemLog": map[string]any{
			"verbosity": nil,
			"logAppend": true,
		},
	}
	prev := map[string]any{
		"systemLog": map[string]any{
			"verbosity": 4,
			"logAppend": true,
		},
	}
	expected := map[string]any{
		"systemLog": map[string]any{
			"logAppend": true,
		},
	}
	actual := RemoveFieldsBasedOnDesiredAndPrevious(current, desired, prev)
	assert.Equal(t, expected, actual, "verbosity: nil in the desired map should remove the field")
}
