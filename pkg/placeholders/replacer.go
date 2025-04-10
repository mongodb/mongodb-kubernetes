package placeholders

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/xerrors"
)

// New returns a new instance of Replacer initialized with values for placeholders (it may be nil or empty).
// Keys in placeholderValues map are just bare placeholder names, without curly braces.
// Example:
//
//	replacer := New(map[string]string{"value1": "v1"})
//	out, replacedFlag, _ := replacer.Process("value1={value1}")
//	// out contains "value1=v1", replacedFlag is true
func New(placeholderValues map[string]string) *Replacer {
	replacer := &Replacer{placeholders: map[string]string{}}
	replacer.addPlaceholderValues(placeholderValues)

	return replacer
}

// Replacer helps in replacing {placeholders} with concrete values in strings.
// It is immutable and thread safe.
type Replacer struct {
	// maps placeholder key to its value
	// keys are stored internally with curly braces {key}
	placeholders map[string]string
}

var placeholderRegex = regexp.MustCompile(`{(\w+)}`)

// addPlaceholderValues is intentionally private to guarantee immutability.
func (r *Replacer) addPlaceholderValues(placeholderValues map[string]string) {
	for placeholder, value := range placeholderValues {
		r.placeholders[fmt.Sprintf("{%s}", placeholder)] = value
	}
}

// Process replaces all {placeholders} in str.
// All placeholders in the string must be replaced. Error is returned if there is no value for a placeholder registered, but the placeholder value may be empty.
// It always returns a copy of str, even if there is no placeholders present.
func (r *Replacer) Process(str string) (string, bool, error) {
	notReplacedKeys := map[string]struct{}{}
	replaceFunc := func(key string) string {
		if value, ok := r.placeholders[key]; ok {
			return value
		}
		notReplacedKeys[key] = struct{}{}
		return ""
	}

	replacedStr := placeholderRegex.ReplaceAllStringFunc(str, replaceFunc)

	if len(notReplacedKeys) > 0 {
		var keys []string
		for key := range notReplacedKeys {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return "", false, xerrors.Errorf("missing values for the following placeholders: %s", strings.Join(keys, ", "))
	}

	return replacedStr, str != replacedStr, nil
}

// ProcessMap returns a copy of strMap with all values passed through Process.
// If any of the values fail to process, the error is returned immediately.
func (r *Replacer) ProcessMap(strMap map[string]string) (map[string]string, bool, error) {
	newMap := map[string]string{}
	mapWasModified := false
	for key, value := range strMap {
		processedValue, modified, err := r.Process(value)
		if err != nil {
			return nil, false, xerrors.Errorf("error replacing placeholders in map with key=%s, value=%s: %w", key, value, err)
		}
		newMap[key] = processedValue
		mapWasModified = mapWasModified || modified
	}

	return newMap, mapWasModified, nil
}
