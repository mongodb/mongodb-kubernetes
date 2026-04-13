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

func TestToFlatMap(t *testing.T) {
	t.Run("flat map is unchanged", func(t *testing.T) {
		m := map[string]interface{}{"a": "foo", "b": 42}
		flat := ToFlatMap(m)
		assert.Equal(t, `"foo"`, flat["a"])
		assert.Equal(t, `42`, flat["b"])
	})
	t.Run("nested keys use dot notation", func(t *testing.T) {
		m := map[string]interface{}{
			"storage": map[string]interface{}{
				"engine": "wiredTiger",
			},
		}
		flat := ToFlatMap(m)
		assert.Equal(t, `"wiredTiger"`, flat["storage.engine"])
		assert.Len(t, flat, 1)
	})
	t.Run("deeply nested map", func(t *testing.T) {
		m := map[string]interface{}{
			"net": map[string]interface{}{
				"tls": map[string]interface{}{
					"mode": "requireTLS",
				},
			},
		}
		flat := ToFlatMap(m)
		assert.Equal(t, `"requireTLS"`, flat["net.tls.mode"])
		assert.Len(t, flat, 1)
	})
	t.Run("empty map", func(t *testing.T) {
		assert.Empty(t, ToFlatMap(map[string]interface{}{}))
	})
	t.Run("boolean and nil leaf values", func(t *testing.T) {
		m := map[string]interface{}{"enabled": true, "missing": nil}
		flat := ToFlatMap(m)
		assert.Equal(t, "true", flat["enabled"])
		assert.Equal(t, "null", flat["missing"])
	})
}

func TestFlatMapsEqual(t *testing.T) {
	t.Run("identical maps are equal", func(t *testing.T) {
		m := map[string]interface{}{"storage": map[string]interface{}{"engine": "wiredTiger"}}
		assert.True(t, FlatMapsEqual(m, m))
	})
	t.Run("different leaf value", func(t *testing.T) {
		a := map[string]interface{}{"storage": map[string]interface{}{"engine": "wiredTiger"}}
		b := map[string]interface{}{"storage": map[string]interface{}{"engine": "inMemory"}}
		assert.False(t, FlatMapsEqual(a, b))
	})
	t.Run("different key count", func(t *testing.T) {
		a := map[string]interface{}{"a": "1", "b": "2"}
		b := map[string]interface{}{"a": "1"}
		assert.False(t, FlatMapsEqual(a, b))
	})
	t.Run("empty maps are equal", func(t *testing.T) {
		assert.True(t, FlatMapsEqual(map[string]interface{}{}, map[string]interface{}{}))
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
