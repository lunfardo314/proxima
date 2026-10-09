//go:build linux

package mine

import (
	"os/exec"
	"syscall"
)

// setParentDeathSignal ends the seeker when the miner dies in a way that skips
// its own cleanup, such as a kill signal: the kernel sends the child this
// signal when its parent thread exits.
func setParentDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
