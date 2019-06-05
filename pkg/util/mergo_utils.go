package util

import (
	"encoding/json"
	"reflect"

	"github.com/imdario/mergo"
)

// MergoDelete is a sentinel value that indicates a field is to be removed during the merging process
const MergoDelete = "MERGO_DELETE"

// AgentTransformer when we want to delete the last element of a list, if we use the
// default behaviour we will still be left with the final element from the original map. Using this
// Transformer allows us to override that behaviour and perform the merging as we expect.
type AgentTransformer struct{}

func (t AgentTransformer) Transformer(typ reflect.Type) func(dst, src reflect.Value) error {
	return func(dst, src reflect.Value) error {
		dstMap := dst.Interface().(map[string]interface{})
		toDelete := make([]string, 0)
		for key, value := range dstMap {
			// marking a value as "util.MergoDelete" means we want to delete the field
			if value == MergoDelete {
				toDelete = append(toDelete, key)
			}
		}

		for key, val := range src.Interface().(map[string]interface{}) {
			if _, ok := dstMap[key]; !ok {
				switch val.(type) {
				// if a list is absent on the dst, it means we want to delete it
				case []interface{}:
					dstMap[key] = make([]interface{}, 0)
				default: // otherwise we can take the value being set
					dstMap[key] = val
				}
			}
		}

		for _, key := range toDelete {
			delete(dstMap, key)
		}

		return nil
	}
}

// MergeWith takes a structToMerge, a source map src, and returns the result of the merging, and an error
func MergeWith(structToMerge interface{}, src map[string]interface{}, transformers mergo.Transformers) (map[string]interface{}, error) {
	bytes, err := json.Marshal(structToMerge)
	if err != nil {
		return nil, err
	}

	dst := make(map[string]interface{}, 0)
	err = json.Unmarshal(bytes, &dst)
	if err != nil {
		return nil, err
	}

	mergo.Merge(&dst, src, mergo.WithTransformers(transformers))

	return dst, nil
}
