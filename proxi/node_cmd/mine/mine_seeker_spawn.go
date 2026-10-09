package mine

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/proxi/glb"
)

// Spawning the reference nonce seeker (kb/external_nonce_seeker.md).
//
// A seeker is a separate program so that it can run on another machine. On
// the same machine that separation is only wiring to get wrong: the listener
// to configure, the port and token to repeat, the key file and its passphrase
// to supply twice, the binary to find. With mine.seeker.spawn the miner does
// the wiring: it listens on a free loopback port with a fresh token, starts
// the binary with every argument filled in, hands it the passphrase it
// unlocked the key with, logs its output, restarts it when it dies and takes
// it down when the miner goes. The protocol between the two is the one
// remote seekers use.

const (
	seekerDefaultBinary = "nonce_seeker"
	seekerRestartBase   = time.Second
	seekerRestartMax    = 30 * time.Second
)

// resolveSeekerBinary finds the seeker to spawn: the configured path as given,
// or the default name on PATH, or beside the running proxi executable.
func resolveSeekerBinary(configured string) (string, error) {
	if configured != "" {
		if p, err := exec.LookPath(configured); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("nonce seeker binary '%s' (mine.seeker.binary) not found", configured)
	}
	if p, err := exec.LookPath(seekerDefaultBinary); err == nil {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), seekerDefaultBinary)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("nonce seeker binary '%s' not found on PATH or beside proxi: build it with 'cargo build --release' "+
		"in proxi/node_cmd/mine/nonce_seeker, then put it on PATH or name it in mine.seeker.binary", seekerDefaultBinary)
}

// seekerArgs is the command line of a seeker dialing the miner's job server.
// Zero threads leaves the seeker its default, every core.
func seekerArgs(proxiURL, token, keyFile string, threads int, name string) []string {
	args := []string{"--proxi", proxiURL, "--key-file", keyFile, "--name", name}
	if token != "" {
		args = append(args, "--token", token)
	}
	if threads > 0 {
		args = append(args, "--threads", strconv.Itoa(threads))
	}
	return args
}

// randomToken is the bearer token of a spawned seeker: the listener is
// loopback, the token keeps other local processes off the result endpoint.
func randomToken() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	glb.AssertNoError(err)
	return hex.EncodeToString(b[:])
}

// seekerSpawn runs one seeker process for the life of the miner.
type seekerSpawn struct {
	binary      string
	args        []string
	env         []string // added to the miner's own environment
	logf        func(format string, args ...any)
	restartBase time.Duration

	mu      sync.Mutex
	cmd     *exec.Cmd
	stop    chan struct{}
	stopped chan struct{}
}

func newSeekerSpawn(binary string, args []string, passphrase string) *seekerSpawn {
	sp := &seekerSpawn{
		binary:      binary,
		args:        args,
		logf:        glb.Infof,
		restartBase: seekerRestartBase,
		stop:        make(chan struct{}),
		stopped:     make(chan struct{}),
	}
	if passphrase != "" {
		sp.env = []string{"PROXIMA_KEY_PASSPHRASE=" + passphrase}
	}
	return sp
}

// run starts the seeker and keeps it running until stop: a seeker that exits
// is restarted with a backoff that resets after a run of some length. Its
// output goes to the miner's log, line by line. Blocks; meant for a goroutine.
func (sp *seekerSpawn) run() {
	defer close(sp.stopped)
	// the parent-death signal is tied to the OS thread that forks the child; pinning
	// the goroutine to one thread for its whole life makes that thread's end the
	// miner's end, not a scheduler's whim
	runtime.LockOSThread()
	delay := sp.restartBase
	for {
		startedAt := time.Now()
		err := sp.runOnce()
		select {
		case <-sp.stop:
			return
		default:
		}
		if time.Since(startedAt) > seekerRestartMax {
			delay = sp.restartBase
		}
		sp.logf("nonce seeker exited (%v); restarting in %v", err, delay)
		select {
		case <-sp.stop:
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > seekerRestartMax {
			delay = seekerRestartMax
		}
	}
}

// runOnce starts the process and waits for it to end.
func (sp *seekerSpawn) runOnce() error {
	cmd := exec.Command(sp.binary, sp.args...)
	cmd.Env = append(os.Environ(), sp.env...)
	setParentDeathSignal(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err = cmd.Start(); err != nil {
		return err
	}
	sp.mu.Lock()
	sp.cmd = cmd
	sp.mu.Unlock()
	sp.logf("nonce seeker started: %s (pid %d)", sp.binary, cmd.Process.Pid)
	sp.forward(stdout)
	return cmd.Wait()
}

// forward copies the seeker's output into the log, one line per message.
func (sp *seekerSpawn) forward(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			sp.logf("[seeker] %s", line)
		}
	}
}

// kill ends the seeker and the restart loop; safe to call more than once.
func (sp *seekerSpawn) kill() {
	select {
	case <-sp.stop:
		return
	default:
		close(sp.stop)
	}
	sp.mu.Lock()
	cmd := sp.cmd
	sp.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-sp.stopped
}
