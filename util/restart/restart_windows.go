//go:build windows

package restart

import (
	"os"
	"os/exec"
)

func init() {
	SelfRestart = selfRestart
}

func selfRestart(binary string, args []string, env []string) error {
	// Windows doesn't support syscall.Exec, spawn new process and exit
	cmd := exec.Command(binary, args[1:]...) // args[0] is the program name
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return err
	}
	// Exit current process; new process continues independently
	os.Exit(0)
	return nil // unreachable
}
