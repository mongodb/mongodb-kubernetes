package maputil

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/spf13/cast"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
)

// ReadMapValueAsInterface traverses the nested maps inside the 'm' map following the 'keys' path and returns the last element
// as an 'interface{}'
func ReadMapValueAsInterface(m map[string]interface{}, keys ...string) interface{} {
	currentMap := m
	for i, k := range keys {
		if _, ok := currentMap[k]; !ok {
			return nil
		}
		if i == len(keys)-1 {
			return currentMap[k]
		}
		currentMap = currentMap[k].(map[string]interface{})
	}
	return nil
}

// ReadMapValueAsString traverses the nested maps inside the 'm' map following the 'keys' path and returns the last element
// as a 'string'
func ReadMapValueAsString(m map[string]interface{}, keys ...string) string {
	res := ReadMapValueAsInterface(m, keys...)

	if res == nil {
		return ""
	}
	return res.(string)
}

func ReadMapValueAsInt(m map[string]interface{}, keys ...string) int {
	res := ReadMapValueAsInterface(m, keys...)
	if res == nil {
		return 0
	}
	return cast.ToInt(res)
}

// ReadMapValueAsMap traverses the nested maps inside the 'm' map following the 'keys' path and returns the last element
// as a 'map[string]interface{}'
func ReadMapValueAsMap(m map[string]interface{}, keys ...string) map[string]interface{} {
	res := ReadMapValueAsInterface(m, keys...)

	if res == nil {
		return nil
	}
	return res.(map[string]interface{})
}

// ToFlatList returns all elements as a sorted list of string values.
// It performs a recursive traversal of maps and dumps the current config to the final list of configs
func ToFlatList(m map[string]interface{}) []string {
	result := traverse(m, []string{})
	sort.Strings(result)
	return result
}

// SetMapValue traverses the nested maps inside the 'm' map following the 'keys' path and sets the value 'value' to the
// final key. The key -> nested map entry will be created if doesn't exist
func SetMapValue(m map[string]interface{}, value interface{}, keys ...string) {
	current := m
	for _, k := range keys[0 : len(keys)-1] {
		if _, ok := current[k]; !ok {
			current[k] = map[string]interface{}{}
		}
		current = current[k].(map[string]interface{})
	}
	last := keys[len(keys)-1]
	current[last] = value
}

func DeleteMapValue(m map[string]interface{}, keys ...string) {
	current := m
	for _, k := range keys[0 : len(keys)-1] {
		if _, ok := current[k]; !ok {
			current[k] = map[string]interface{}{}
		}
		current = current[k].(map[string]interface{})
	}
	delete(current, keys[len(keys)-1])
}

func traverse(currentValue interface{}, currentPath []string) []string {
	switch v := currentValue.(type) {
	case map[string]interface{}:
		{
			var allPaths []string
			for key, value := range v {
				allPaths = append(allPaths, traverse(value, append(currentPath, key))...)
			}
			return allPaths
		}
	default:
		{
			// We found the "terminal" node in the map - need to dump the current path
			path := strings.Join(currentPath, ".")
			return []string{path}
		}
	}
}

// RemoveFieldsBasedOnDesiredAndPrevious returns a "currentMap" that has had fields removed based on what was in the previousMap
// and what is in the desiredMap. Any values that were there previously, but are no longer desired, will be removed and the
// resulting map will not contain them.
func RemoveFieldsBasedOnDesiredAndPrevious(currentMap, desiredMap, previousMap map[string]interface{}) map[string]interface{} {
	if desiredMap == nil {
		desiredMap = map[string]interface{}{}
	}

	if previousMap == nil {
		previousMap = map[string]interface{}{}
	}

	desiredFlatList := ToFlatList(desiredMap)
	previousFlatList := ToFlatList(previousMap)

	var itemsToRemove []string
	for _, item := range previousFlatList {
		// if an item was set previously, but is not set now
		// it means we want to remove this item.
		if !stringutil.Contains(desiredFlatList, item) {
			itemsToRemove = append(itemsToRemove, item)
		}
	}

	for _, item := range itemsToRemove {
		DeleteMapValue(currentMap, strings.Split(item, ".")...)
	}

	return currentMap
}

// StructToMap is a function to convert struct to map using JSON tags
func StructToMap(v interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}
