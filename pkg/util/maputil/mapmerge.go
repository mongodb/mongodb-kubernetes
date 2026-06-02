package maputil

import "github.com/imdario/mergo"

func MergeMaps(dst, src map[string]interface{}) {
	if dst == nil || src == nil {
		return
	}
	// mergo.Merge errors only on type mismatches between non-map types, which can't happen with map[string]interface{}
	_ = mergo.Merge(&dst, src, mergo.WithOverride)
}
