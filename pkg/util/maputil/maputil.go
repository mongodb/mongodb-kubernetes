package maputil

import "github.com/spf13/cast"

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

func ReadMapValueAsFloat64(m map[string]interface{}, keys ...string) float64 {
	res := ReadMapValueAsInterface(m, keys...)
	if res == nil {
		return 0
	}
	return res.(float64)
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
