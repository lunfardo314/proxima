package restart

// SelfRestart restarts the current process.
// On Unix (Linux/Darwin): uses syscall.Exec to replace the current process (same PID)
// On Windows: spawns a new process and exits the current one (new PID)
// Returns error if restart fails; on success, this function does not return on Unix
var SelfRestart func(binary string, args []string, env []string) error
