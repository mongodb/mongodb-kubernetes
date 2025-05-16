package maputil

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type SomeType string

const (
	TypeMongos SomeType = "mongos"
)

func TestMergeMaps(t *testing.T) {
	t.Run("Merge to empty map", func(t *testing.T) {
		dst := map[string]interface{}{}
		src := mapForTest()
		MergeMaps(dst, src)

		assert.Equal(t, dst, src)

		// mutation of the initial map doesn't change the destination
		ReadMapValueAsMap(src, "nestedMap")["key4"] = 80
		assert.Equal(t, int32(30), ReadMapValueAsInterface(dst, "nestedMap", "key4"))
	})
	t.Run("Merge overrides only common fields", func(t *testing.T) {
		dst := map[string]interface{}{
			"key1":   "old value",                              // must be overridden
			"key2":   map[string]interface{}{"rubbish": "yes"}, // completely different type - will be overridden
			"oldkey": "must retain!",
			"nestedMap": map[string]interface{}{
				"key4": 100, // the destination type is int - will be cast from int32
				"nestedNestedMap": map[string]interface{}{
					"key7":             float32(100.55), // the destination type is float32 - will be cast
					"key8":             "mongod",        // will be overridden by TypeMongos
					"key9":             []string{"old"}, // must be overridden
					"oldkey2":          []int{1},
					"anotherNestedMap": map[string]interface{}{},
				},
			},
		}
		src := mapForTest()
		MergeMaps(dst, src)

		expected := mapForTest()
		expected["oldkey"] = "must retain!"
		ReadMapValueAsMap(expected, "nestedMap")["key4"] = 30
		ReadMapValueAsMap(expected, "nestedMap", "nestedNestedMap")["key7"] = float32(40.56)
		ReadMapValueAsMap(expected, "nestedMap", "nestedNestedMap")["oldkey2"] = []int{1}
		ReadMapValueAsMap(expected, "nestedMap", "nestedNestedMap")["anotherNestedMap"] = map[string]interface{}{}

		assert.Equal(t, expected, dst)

		// mutation of the initial map doesn't change the destination
		src["nestedMap"].(map[string]interface{})["newkey"] = 80
		assert.Empty(t, ReadMapValueAsInterface(dst, "nestedMap", "newkey"))
	})
	t.Run("Fails if destination is not map", func(t *testing.T) {
		dst := map[string]interface{}{"nestedMap": "foo"}
		src := mapForTest()
		assert.Panics(t, func() { MergeMaps(dst, src) })
	})
	t.Run("Pointers are not copied", func(t *testing.T) {
		dst := map[string]interface{}{}
		src := mapForTest()
		MergeMaps(dst, src)

		pointer := ReadMapValueAsInterface(src, "nestedMap", "nestedNestedMap", "key11").(*int32)
		*pointer = 20

		// destination map has changed as well as we don't copy pointers, just reassign them
		assert.Equal(t, util.Int32Ref(20), ReadMapValueAsInterface(dst, "nestedMap", "nestedNestedMap", "key11"))
	})
}

func mapForTest() map[string]interface{} {
	return map[string]interface{}{
		"key1": "value1",
		"key2": int8(10),
		"key3": int16(20),
		"nestedMap": map[string]interface{}{
			"key4": int32(30),
			"key5": int64(40),
			"nestedNestedMap": map[string]interface{}{
				"key6":  float32(40.56),
				"key7":  float64(40.56),
				"key8":  TypeMongos,
				"key9":  []string{"one", "two"},
				"key10": true,
				"key11": util.Int32Ref(10),
			},
		},
	}
}
