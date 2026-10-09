package mine

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The spawner is exercised with this test binary as the seeker: the helper
// process below prints its arguments and whether the passphrase reached it,
// then exits, so one test covers the command line, the passphrase hand-off,
// the output forwarding and the restart on exit.
const seekerHelperEnv = "PROXI_TEST_SEEKER_HELPER"

func TestSeekerHelperProcess(t *testing.T) {
	if os.Getenv(seekerHelperEnv) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	fmt.Printf("helper args: %s\n", strings.Join(args, " "))
	fmt.Printf("helper passphrase set: %v\n", os.Getenv("PROXIMA_KEY_PASSPHRASE") != "")
	time.Sleep(100 * time.Millisecond)
	os.Exit(0)
}

func TestSeekerSpawnRunsAndRestarts(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	joined := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}

	args := append([]string{"-test.run=^TestSeekerHelperProcess$", "--"},
		seekerArgs("http://127.0.0.1:8100", "tok", "proxima.key", 3, "test")...)
	sp := newSeekerSpawn(os.Args[0], args, "secret")
	sp.env = append(sp.env, seekerHelperEnv+"=1")
	sp.logf = logf
	sp.restartBase = 50 * time.Millisecond
	go sp.run()

	// the helper exits at once, so a second start proves the restart
	require.Eventually(t, func() bool {
		return strings.Count(joined(), "nonce seeker started") >= 2
	}, 5*time.Second, 20*time.Millisecond, "the seeker is restarted after it exits")
	sp.kill()

	out := joined()
	require.Contains(t, out, "[seeker] helper args: --proxi http://127.0.0.1:8100 --key-file proxima.key --name test --token tok --threads 3")
	require.Contains(t, out, "[seeker] helper passphrase set: true")
	require.Contains(t, out, "restarting in")
}

// kill on a spawner that never started returns at once
func TestSeekerSpawnKillBeforeRun(t *testing.T) {
	sp := newSeekerSpawn("/nonexistent/seeker", nil, "")
	go sp.run()
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() { sp.kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("kill did not return")
	}
}

// the command line carries the token and the threads only when given
func TestSeekerArgs(t *testing.T) {
	require.Equal(t, []string{"--proxi", "http://h:1", "--key-file", "k", "--name", "n"}, seekerArgs("http://h:1", "", "k", 0, "n"))
	require.Equal(t, []string{"--proxi", "http://h:1", "--key-file", "k", "--name", "n", "--token", "t", "--threads", "4"}, seekerArgs("http://h:1", "t", "k", 4, "n"))
}
