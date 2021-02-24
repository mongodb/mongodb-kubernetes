package stringutil

import "strings"

// Ref is a convenience function which returns
// a reference to the provided string
func Ref(s string) *string {
	return &s
}

// Contains returns true if there is at least one string in `slice`
// that is equal to `s`.
func Contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func ContainsAny(slice []string, ss ...string) bool {
	for _, s := range ss {
		if Contains(slice, s) {
			return true
		}
	}

	return false
}

func Remove(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

// UpperCaseFirstChar ensures the message first char is uppercased.
func UpperCaseFirstChar(msg string) string {
	if msg == "" {
		return ""
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}
