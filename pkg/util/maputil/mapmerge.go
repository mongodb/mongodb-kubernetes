package maputil

import (
	"github.com/spf13/cast"
)

// MergeMaps is a simplified function to merge one generic map into another recursively. It doesn't use reflection.
// It has some known limitations triggered by use case mainly (as it is used for mongodb options which have quite simple
// structure):
// - slices are not copied recursively
// - pointers are overridden
//
// Dev note: it's used instead of 'mergo' as the latter one is very restrictive in merged types
// (float32 vs float64, string vs string type etc) so works poorly with merging in-memory maps to the unmarshalled ones
// Also it's possible to add visitors functionality for flexible merging if necessary
func MergeMaps(dst, src map[string]interface{}) {
	if dst == nil {
		return
	}
	if src == nil {
		return
	}
	for key, srcValue := range src {
		switch t := srcValue.(type) {
		case map[string]interface{}:
			{
				if _, ok := dst[key]; !ok {
					dst[key] = map[string]interface{}{}
				}
				// this will fall if the destination value is not map - this is fine
				dstMap := dst[key].(map[string]interface{})
				MergeMaps(dstMap, t)
			}
		default:
			{
				dst[key] = castValue(dst[key], t)
			}
		}
	}
}

// Note, that currently slices will not be copied - this is OK for current use cases (mongodb options almost don't
// use arrays). All strings will be copied.
func castValue(dst interface{}, src interface{}) interface{} {
	switch dst.(type) {
	case int:
		return cast.ToInt(src)
	case int8:
		return cast.ToInt8(src)
	case int16:
		return cast.ToInt16(src)
	case int32:
		return cast.ToInt32(src)
	case int64:
		return cast.ToInt64(src)
	case float32:
		return cast.ToFloat32(src)
	case float64:
		return cast.ToFloat64(src)
	default:
		return src
	}
}
