package operator

// This is a collection of some utility/common methods that may be shared by other go source code

// MakeInReference is required to return a *int32, which can't be declared as a literal.
func Int32Ref(i int32) *int32 {
	return &i
}
