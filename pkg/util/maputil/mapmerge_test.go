package maputil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type SomeType string

const (
	TypeMongos SomeType = "mongos"
)

func TestMergeMaps(t *testing.T) {
	t.Run("Merge to empty map", func(t *testing.T) {
		dst := map[string]any{}
		src := mapForTest()
		MergeMaps(dst, src)

		assert.Equal(t, dst, src)
	})
	t.Run("Merge overrides only common fields", func(t *testing.T) {
		dst := map[string]any{
			"key1":   "old value",                      // must be overridden
			"key2":   map[string]any{"rubbish": "yes"}, // completely different type - will be overridden
			"oldkey": "must retain!",
			"nestedMap": map[string]any{
				"key4": 100,
				"nestedNestedMap": map[string]any{
					"key7":             float32(100.55),
					"key8":             "mongod",        // will be overridden by TypeMongos
					"key9":             []string{"old"}, // must be overridden
					"oldkey2":          []int{1},
					"anotherNestedMap": map[string]any{},
				},
			},
		}
		src := mapForTest()
		MergeMaps(dst, src)

		expected := mapForTest()
		expected["oldkey"] = "must retain!"
		// mergo preserves source types rather than coercing to destination types:
		// key4 stays int32 (src type), key7 stays float64 (src type)
		ReadMapValueAsMap(expected, "nestedMap", "nestedNestedMap")["oldkey2"] = []int{1}
		ReadMapValueAsMap(expected, "nestedMap", "nestedNestedMap")["anotherNestedMap"] = map[string]any{}

		assert.Equal(t, expected, dst)
	})
	t.Run("Nil src value deletes key from dst", func(t *testing.T) {
		dst := map[string]any{
			"systemLog": map[string]any{
				"verbosity": 4,
				"logAppend": true,
			},
		}
		src := map[string]any{
			"systemLog": map[string]any{
				"verbosity": nil,
				"logAppend": true,
			},
		}
		MergeMaps(dst, src)
		_, hasVerbosity := dst["systemLog"].(map[string]any)["verbosity"]
		assert.False(t, hasVerbosity, "nil src value must remove the key from dst")
		assert.Equal(t, true, dst["systemLog"].(map[string]any)["logAppend"])
	})
	t.Run("Nil src value is no-op when key already absent from dst", func(t *testing.T) {
		dst := map[string]any{
			"systemLog": map[string]any{
				"logAppend": true,
			},
		}
		src := map[string]any{
			"systemLog": map[string]any{
				"verbosity": nil,
				"logAppend": true,
			},
		}
		MergeMaps(dst, src)
		_, hasVerbosity := dst["systemLog"].(map[string]any)["verbosity"]
		assert.False(t, hasVerbosity)
		assert.Equal(t, true, dst["systemLog"].(map[string]any)["logAppend"])
	})
	t.Run("Pointers are not copied", func(t *testing.T) {
		dst := map[string]any{}
		src := mapForTest()
		MergeMaps(dst, src)

		pointer := ReadMapValueAsInterface(src, "nestedMap", "nestedNestedMap", "key11").(*int32)
		*pointer = 20

		// destination map has changed as well as we don't copy pointers, just reassign them
		assert.Equal(t, new(int32(20)), ReadMapValueAsInterface(dst, "nestedMap", "nestedNestedMap", "key11"))
	})
}

func mapForTest() map[string]any {
	return map[string]any{
		"key1": "value1",
		"key2": int8(10),
		"key3": int16(20),
		"nestedMap": map[string]any{
			"key4": int32(30),
			"key5": int64(40),
			"nestedNestedMap": map[string]any{
				"key6":  float32(40.56),
				"key7":  float64(40.56),
				"key8":  TypeMongos,
				"key9":  []string{"one", "two"},
				"key10": true,
				"key11": new(int32(10)),
			},
		},
	}
}
