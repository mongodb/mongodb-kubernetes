package mongodbproxy

import (
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
)

// prSetThpDisable is PR_SET_THP_DISABLE from <linux/prctl.h>.
const prSetThpDisable = 41

// startWithTHPDisabled starts cmd with transparent huge pages disabled for the child.
//
// The flag is inherited across fork and exec. Like the automation agent, we set it right before
// the start and reset it right after, so it does not outlive the launch. Starting happens on a
// locked OS thread because the caller must not be moved between the two prctl calls.
func startWithTHPDisabled(cmd *exec.Cmd) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := prctlTHP(1); err != nil {
		return fmt.Errorf("disabling THP: %w", err)
	}
	startErr := cmd.Start()
	if err := prctlTHP(0); err != nil && startErr == nil {
		return fmt.Errorf("resetting THP flag after start: %w", err)
	}
	return startErr
}

func prctlTHP(value uintptr) error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetThpDisable, value, 0); errno != 0 {
		return errno
	}
	return nil
}
