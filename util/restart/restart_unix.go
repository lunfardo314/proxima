//go:build linux || darwin

package restart

import (
	"syscall"
)

func init() {
	SelfRestart = selfRestart
}

func selfRestart(binary string, args []string, env []string) error {
	// syscall.Exec replaces current process with new one (same PID)
	return syscall.Exec(binary, args, env)
}
