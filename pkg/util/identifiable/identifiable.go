package identifiable

import (
	"reflect"
)

// Identifiable is a simple interface wrapping any object which has some key field which can be used for later
// aggregation operations (grouping, intersection, difference etc)
type Identifiable interface {
	Identifier() interface{}
}

// SetDifference returns all 'Identifiable' elements that are in left slice and not in the right one
func SetDifference(left, right []Identifiable) []Identifiable {
	result := make([]Identifiable, 0)
	for _, l := range left {
		found := false
		for _, r := range right {
			if r.Identifier() == l.Identifier() {
				found = true
				break
			}
		}
		if !found {
			result = append(result, l)
		}
	}
	return result
}

// SetIntersection returns all 'Identifiable' elements from 'left' and 'right' slice that intersect by 'Identifier()'
// value. Each intersection is represented as a tuple of two elements - matching elements from 'left' and 'right'
func SetIntersection(left, right []Identifiable) [][]Identifiable {
	result := make([][]Identifiable, 0)
	for _, l := range left {
		for _, r := range right {
			if r.Identifier() == l.Identifier() {
				result = append(result, []Identifiable{l, r})
			}
		}
	}
	return result
}

// SetDifferenceGeneric is a convenience function solving lack of covariance in Go: it allows to pass the arrays declared
// as some types implementing 'Identifiable' and find the difference between them
// Important: the arrays past must declare types implementing 'Identifiable'!
func SetDifferenceGeneric(left, right interface{}) []Identifiable {
	leftIdentifiers := toIdentifiableSlice(left)
	rightIdentifiers := toIdentifiableSlice(right)

	return SetDifference(leftIdentifiers, rightIdentifiers)
}

// SetIntersectionGeneric is a convenience function solving lack of covariance in Go: it allows to pass the arrays declared
// as some types implementing 'Identifiable' and find the intersection between them
// Important: the arrays past must declare types implementing 'Identifiable'!
func SetIntersectionGeneric(left, right interface{}) [][]Identifiable {
	leftIdentifiers := toIdentifiableSlice(left)
	rightIdentifiers := toIdentifiableSlice(right)

	// check if there is a difference in the config with same ID
	found := false
	for _, l := range leftIdentifiers {
		for _, r := range rightIdentifiers {
			if l.Identifier() == r.Identifier() {
				if l != r {
					found = true
					break
				}
			}
		}
	}
	if !found {
		return nil
	}

	return SetIntersection(leftIdentifiers, rightIdentifiers)
}

// toIdentifiableSlice uses reflection to cast the array
func toIdentifiableSlice(data interface{}) []Identifiable {
	value := reflect.ValueOf(data)

	result := make([]Identifiable, value.Len())
	for i := 0; i < value.Len(); i++ {
		result[i] = value.Index(i).Interface().(Identifiable)
	}
	return result
}
