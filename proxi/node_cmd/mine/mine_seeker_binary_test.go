package mine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/lunfardo314/proxima/util/vrf"
	"github.com/stretchr/testify/require"
)

// Interoperability of the reference seeker (proxi/node_cmd/mine/nonce_seeker,
// Rust) with the miner's job server: the binary is pointed at a server with a
// real key and a small K and must deliver an accepted, verifiable solution and
// report its attempts. Runs only when NONCE_SEEKER_BIN names the built binary,
// since a Rust toolchain is not part of the Go build:
//
//	(cd proxi/node_cmd/mine/nonce_seeker && cargo build --release)
//	NONCE_SEEKER_BIN=$PWD/proxi/node_cmd/mine/nonce_seeker/target/release/nonce_seeker go test -run TestNonceSeekerBinary ./proxi/node_cmd/mine/
func TestNonceSeekerBinary(t *testing.T) {
	bin := os.Getenv("NONCE_SEEKER_BIN")
	if bin == "" {
		t.Skip("NONCE_SEEKER_BIN not set")
	}
	const passphrase = "a passphrase of some length"

	run := func(t *testing.T, encrypted bool) {
		e := newSeekerTestEnv(t, "tok")
		dir := t.TempDir()
		keyFile := filepath.Join(dir, "proxima.key")
		sk := e.sk
		var ks *keystore.Keystore
		var err error
		if encrypted {
			ks, err = keystore.Encrypt(keystore.KeyTypeED25519, sk, e.pk, passphrase, "holder")
		} else {
			ks, err = keystore.NewUnencrypted(keystore.KeyTypeED25519, sk, e.pk, "holder")
		}
		require.NoError(t, err)
		b, err := json.Marshal(ks)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(keyFile, b, 0o600))

		cmd := exec.Command(bin, "--proxi", e.http.URL, "--token", "tok", "--key-file", keyFile, "--threads", "2", "--name", "rust")
		cmd.Env = append(os.Environ(), "PROXIMA_KEY_PASSPHRASE="+passphrase)
		out, err := os.Create(filepath.Join(dir, "seeker.log"))
		require.NoError(t, err)
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			logBytes, _ := os.ReadFile(filepath.Join(dir, "seeker.log"))
			t.Logf("seeker log:\n%s", logBytes)
		})

		// One job for the whole round. The first solution is accepted and the
		// seeker searches on in the same job, under the beat the answer gave
		// it, so the next solution arrives with no new job published.
		const slot, k = 42, 10
		results := e.srv.publish(e.pred, slot, k, nil, time.Now().Add(time.Minute))
		var prev []byte
		for i := 0; i < 2; i++ {
			var sol mineSolution
			select {
			case sol = <-results:
			case <-time.After(60 * time.Second):
				t.Fatalf("no solution %d from the seeker in 60s", i+1)
			}
			beta, err := vrf.Verify(e.pk, txbuildercore.MineVRFMessage(e.pred, slot, sol.nonce), sol.proof)
			require.NoError(t, err)
			require.GreaterOrEqual(t, trailingZeroBits(beta), k)
			if prev != nil {
				require.Less(t, string(beta), string(prev), "an improvement must beat the previous solution")
			}
			prev = beta
		}
		e.srv.retire()

		// attempts arrive by report or with the results
		deadline := time.Now().Add(5 * time.Second)
		for e.srv.pendingAttempts() == 0 && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		require.NotZero(t, e.srv.pendingAttempts())
		require.Contains(t, e.srv.summary(), "rust ")
	}
	t.Run("unencrypted key", func(t *testing.T) { run(t, false) })
	t.Run("encrypted key", func(t *testing.T) { run(t, true) })
}
