package workflow

import "slices"

// RunInGivenOrder will execute N functions, passed as varargs as `funcs`. The order of execution will depend on the result
// of the evaluation of the `shouldRunInOrder` boolean value. If `shouldRunInOrder` is true, the functions will be executed in order; if
// `shouldRunInOrder` is false, the functions will be executed in reverse order (from last to first)
func RunInGivenOrder(shouldRunInOrder bool, funcs ...func() Status) Status {
	if shouldRunInOrder {
		for _, fn := range funcs {
			if status := fn(); !status.IsOK() {
				return status
			}
		}
	} else {
		for _, fn := range slices.Backward(funcs) {
			if status := fn(); !status.IsOK() {
				return status
			}
		}
	}
	return OK()
}
