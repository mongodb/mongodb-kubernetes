//go:build tools
// +build tools

package tools

// forcing these packages to be imported in vendor by `go mod vendor`
import (
	// test code for unit tests
	_ "k8s.io/client-go/discovery/fake"
	_ "k8s.io/client-go/testing"

	// code-generator that does not support go modules yet
	_ "k8s.io/code-generator"
)
