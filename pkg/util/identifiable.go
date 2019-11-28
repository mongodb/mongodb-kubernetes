package util

// Identifiable is a simple interface wrapping any object which has some key field which can be used for later
// aggregation operations (grouping, intersection, difference etc)
type Identifiable interface {
	Identifier() interface{}
}
