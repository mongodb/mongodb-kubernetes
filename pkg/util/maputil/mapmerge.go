package maputil

import "github.com/imdario/mergo"

func MergeMaps(dst, src map[string]interface{}) {
	if dst == nil || src == nil {
		return
	}
	_ = mergo.Merge(&dst, src, mergo.WithOverride)
	removeNilKeys(dst, src)
}

// removeNilKeys deletes keys from dst where the corresponding src value is nil.
// A nil src value is the convention for "remove this key from the merged result".
func removeNilKeys(dst, src map[string]interface{}) {
	for key, srcVal := range src {
		if srcVal == nil {
			delete(dst, key)
			continue
		}
		if srcMap, ok := srcVal.(map[string]interface{}); ok {
			if dstMap, ok := dst[key].(map[string]interface{}); ok {
				removeNilKeys(dstMap, srcMap)
			}
		}
	}
}
