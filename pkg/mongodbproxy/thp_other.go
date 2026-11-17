//go:build !linux

package mongodbproxy

import (
	"errors"
	"os/exec"
)

// startWithTHPDisabled is only supported on Linux, where the proxy actually runs.
func startWithTHPDisabled(_ *exec.Cmd) error {
	return errors.New("disabling THP is only supported on Linux")
}
