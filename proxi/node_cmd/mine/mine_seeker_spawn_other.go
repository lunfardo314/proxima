//go:build !linux

package mine

import "os/exec"

// setParentDeathSignal has no portable equivalent outside Linux; the miner's
// own exit path kills the seeker, an abrupt kill of the miner leaves it to
// its own retry loop against a job server that is gone.
func setParentDeathSignal(*exec.Cmd) {}
