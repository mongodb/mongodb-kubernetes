package maputil

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

func ReadMapValueAsString(m map[string]interface{}, keys ...string) string {
	res := ReadMapValueAsInterface(m, keys...)

	if res == nil {
		return ""
	}
	return res.(string)
}

func ReadMapValueAsMap(m map[string]interface{}, keys ...string) map[string]interface{} {
	res := ReadMapValueAsInterface(m, keys...)

	if res == nil {
		return nil
	}
	return res.(map[string]interface{})
}
