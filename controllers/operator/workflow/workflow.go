package workflow

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
		for i := len(funcs) - 1; i >= 0; i-- {
			if status := funcs[i](); !status.IsOK() {
				return status
			}
		}
	}
	return OK()
}
