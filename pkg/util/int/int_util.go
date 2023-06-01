package int

// Min returns the minimum of 2 integer arguments.
func Min(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
