package testutil

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Support for Go tests that check a Rust twin of a Go package against it: the
// Rust crate ships a small line-oriented tool binary, the Go test builds it
// with cargo and drives it over stdin/stdout. Without a Rust toolchain the
// test is skipped, so the Go suite stays runnable on its own.

// CargoBin builds the named binary target of the crate at crateDir in release
// mode and returns its path. The crate's own target directory is used, so
// repeated runs reuse the build.
func CargoBin(t testing.TB, crateDir, bin string) string {
	t.Helper()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		// rustup's default install location is often not on the PATH of a
		// non-login shell
		if home, _ := os.UserHomeDir(); home != "" {
			if p := filepath.Join(home, ".cargo", "bin", "cargo"); fileExists(p) {
				cargo = p
			}
		}
		if cargo == "" {
			t.Skip("cargo not found: Rust equivalence test skipped")
		}
	}
	cmd := exec.Command(cargo, "build", "--release", "--quiet", "--bin", bin)
	cmd.Dir = crateDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo build of %s in %s failed: %v\n%s", bin, crateDir, err, out)
	}
	// absolute, since a tool may be started in another working directory
	p, err := filepath.Abs(filepath.Join(crateDir, "target", "release", bin))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// LineTool is a running tool process answering one output line per input line.
type LineTool struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Scanner
}

// StartLineTool starts bin with dir as its working directory and stops it when
// the test ends.
func StartLineTool(t testing.TB, bin, dir string) *LineTool {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Wait()
	})
	return &LineTool{cmd: cmd, in: in, out: sc}
}

// Call sends one request line and returns the tool's answer line.
func (lt *LineTool) Call(t testing.TB, line string) string {
	t.Helper()
	if _, err := io.WriteString(lt.in, line+"\n"); err != nil {
		t.Fatalf("write to tool: %v", err)
	}
	if !lt.out.Scan() {
		t.Fatalf("tool produced no answer to %q: %v", line, lt.out.Err())
	}
	return strings.TrimSpace(lt.out.Text())
}
