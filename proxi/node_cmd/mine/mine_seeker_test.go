package mine

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util/vrf"
	"github.com/stretchr/testify/require"
)

// The seeker protocol of kb/external_nonce_seeker.md, exercised over a real
// HTTP server: the long-poll, the result verification with each failure code,
// the attempt accounting, the token, and a round of mineParallel with no local
// workers that is solved by a seeker alone.

type seekerTestEnv struct {
	srv    *seekerServer
	prover *vrf.Prover
	sk     ed25519.PrivateKey
	pk     ed25519.PublicKey
	http   *httptest.Server
	pred   base.OutputID
}

func newSeekerTestEnv(t *testing.T, token string) *seekerTestEnv {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	prover, err := vrf.NewProver(sk)
	require.NoError(t, err)
	s := newSeekerServer(prover, pk, token)
	hs := httptest.NewServer(s.handler())
	t.Cleanup(hs.Close)
	var pred base.OutputID
	rand.Read(pred[:])
	return &seekerTestEnv{srv: s, prover: prover, sk: sk, pk: pk, http: hs, pred: pred}
}

// solve finds, by brute force, a nonce whose output has at least k trailing
// zero bits for the target; k is kept tiny.
func (e *seekerTestEnv) solve(t *testing.T, slot uint32, k int) (nonce [txbuildercore.MineNonceLen]byte, beta []byte) {
	for n := uint64(1); ; n++ {
		binary.BigEndian.PutUint64(nonce[:], n)
		b, _, err := e.prover.Output(txbuildercore.MineVRFMessage(e.pred, slot, nonce))
		require.NoError(t, err)
		if trailingZeroBits(b) >= k {
			return nonce, b
		}
	}
}

func (e *seekerTestEnv) do(t *testing.T, method, path string, body any, token string) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, e.http.URL+path, &buf)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSeekerJobAndResult(t *testing.T) {
	e := newSeekerTestEnv(t, "")

	// no job yet: id 0 and nothing else
	code, out := e.do(t, http.MethodGet, "/seeker/job", nil, "")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 0, out["id"])
	require.NotContains(t, out, "k")

	const slot, k = 77, 4
	results := e.srv.publish(e.pred, slot, k, nil, time.Now().Add(time.Minute))
	code, out = e.do(t, http.MethodGet, "/seeker/job", nil, "")
	require.Equal(t, http.StatusOK, code)
	id := uint64(out["id"].(float64))
	require.NotZero(t, id)
	require.EqualValues(t, k, out["k"])
	require.EqualValues(t, slot, out["slot"])
	require.Equal(t, hex.EncodeToString(e.pk), out["pubkey"])
	require.Greater(t, out["ttl_ms"].(float64), 50_000.0)
	require.NotContains(t, out, "beat")
	// alpha_prefix is the message without the nonce
	msg := txbuildercore.MineVRFMessage(e.pred, slot, [txbuildercore.MineNonceLen]byte{})
	require.Equal(t, hex.EncodeToString(msg[:len(msg)-txbuildercore.MineNonceLen]), out["alpha_prefix"])

	nonce, beta := e.solve(t, slot, k)
	post := func(res seekerResult) (int, map[string]any) {
		return e.do(t, http.MethodPost, "/seeker/result", res, "")
	}

	// stale id
	code, _ = post(seekerResult{ID: id + 1, Nonce: hex.EncodeToString(nonce[:])})
	require.Equal(t, http.StatusConflict, code)
	// malformed nonce
	code, out = post(seekerResult{ID: id, Nonce: "abcd"})
	require.Equal(t, http.StatusUnprocessableEntity, code)
	// a seeker that counts from the wrong end: its own beta disagrees
	code, out = post(seekerResult{ID: id, Nonce: hex.EncodeToString(nonce[:]), Beta: hex.EncodeToString(bytes.Repeat([]byte{1}, 64))})
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Contains(t, out["error"], "beta mismatch")
	// a nonce that does not reach k (nonce 0 almost surely; retry if it does)
	var zero [txbuildercore.MineNonceLen]byte
	if b, _, _ := e.prover.Output(txbuildercore.MineVRFMessage(e.pred, slot, zero)); trailingZeroBits(b) < k {
		code, out = post(seekerResult{ID: id, Nonce: hex.EncodeToString(zero[:])})
		require.Equal(t, http.StatusUnprocessableEntity, code)
		require.Contains(t, out["error"], "insufficient zero bits")
	}

	// the real one, with attempts credited to the round and the seeker
	code, out = post(seekerResult{ID: id, Nonce: hex.EncodeToString(nonce[:]), Beta: hex.EncodeToString(beta), Attempts: 1000, Seeker: "t1"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["accepted"])
	select {
	case sol := <-results:
		require.Equal(t, nonce, sol.nonce)
		got, err := vrf.Verify(e.pk, txbuildercore.MineVRFMessage(e.pred, slot, nonce), sol.proof)
		require.NoError(t, err)
		require.Equal(t, beta, got)
	default:
		t.Fatal("accepted solution not delivered to the round")
	}
	require.EqualValues(t, 1000, e.srv.pendingAttempts())
	require.Contains(t, e.srv.summary(), "t1 ")

	// the job is solved: a second solution is late
	code, _ = post(seekerResult{ID: id, Nonce: hex.EncodeToString(nonce[:])})
	require.Equal(t, http.StatusConflict, code)

	// retired: back to no job
	e.srv.retire()
	code, out = e.do(t, http.MethodGet, "/seeker/job", nil, "")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 0, out["id"])
}

// The beat bound of a contested slot: a solution with enough zero bits is still
// refused when its output is not below beat.
func TestSeekerBeat(t *testing.T) {
	e := newSeekerTestEnv(t, "")
	const slot, k = 5, 3
	nonce, beta := e.solve(t, slot, k)

	lower := bytes.Repeat([]byte{0}, 64)
	e.srv.publish(e.pred, slot, k, lower, time.Now().Add(time.Minute))
	_, out := e.do(t, http.MethodGet, "/seeker/job", nil, "")
	require.Equal(t, hex.EncodeToString(lower), out["beat"])
	id := uint64(out["id"].(float64))
	code, out := e.do(t, http.MethodPost, "/seeker/result", seekerResult{ID: id, Nonce: hex.EncodeToString(nonce[:])}, "")
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Contains(t, out["error"], "not below beat")

	higher := bytes.Repeat([]byte{0xff}, 64)
	e.srv.publish(e.pred, slot, k, higher, time.Now().Add(time.Minute))
	_, out = e.do(t, http.MethodGet, "/seeker/job", nil, "")
	id = uint64(out["id"].(float64))
	code, _ = e.do(t, http.MethodPost, "/seeker/result", seekerResult{ID: id, Nonce: hex.EncodeToString(nonce[:]), Beta: hex.EncodeToString(beta)}, "")
	require.Equal(t, http.StatusOK, code)
}

// A long-poll on the current id returns as soon as a new job is published, and
// returns the unchanged job once the wait expires.
func TestSeekerLongPoll(t *testing.T) {
	e := newSeekerTestEnv(t, "")
	e.srv.publish(e.pred, 1, 1, nil, time.Now().Add(time.Minute))
	_, out := e.do(t, http.MethodGet, "/seeker/job", nil, "")
	id := uint64(out["id"].(float64))

	start := time.Now()
	_, out = e.do(t, http.MethodGet, "/seeker/job?after="+itoa(id)+"&wait=300", nil, "")
	require.EqualValues(t, id, out["id"])
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)

	go func() {
		time.Sleep(100 * time.Millisecond)
		e.srv.publish(e.pred, 2, 1, nil, time.Now().Add(time.Minute))
	}()
	start = time.Now()
	_, out = e.do(t, http.MethodGet, "/seeker/job?after="+itoa(id)+"&wait=5000", nil, "")
	require.NotEqualValues(t, id, out["id"])
	require.EqualValues(t, 2, out["slot"])
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestSeekerToken(t *testing.T) {
	e := newSeekerTestEnv(t, "secret")
	code, _ := e.do(t, http.MethodGet, "/seeker/job", nil, "")
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = e.do(t, http.MethodGet, "/seeker/job", nil, "wrong")
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = e.do(t, http.MethodPost, "/seeker/report", seekerReport{Attempts: 5, Seeker: "x"}, "secret")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 5, e.srv.takeAttempts())
	require.EqualValues(t, 0, e.srv.takeAttempts())
}

// A round with no local workers is held open by the seeker waiter and ends
// with the seeker's solution; the reported attempts land in the round total.
func TestMineParallelSeekerOnly(t *testing.T) {
	e := newSeekerTestEnv(t, "")
	m := &miner{prover: e.prover, workers: 0, seekers: e.srv}
	const slot, k = 9, 3
	nonce, beta := e.solve(t, slot, k)

	go func() {
		// wait for the job, then report attempts and post the solution
		var out map[string]any
		for {
			_, out = e.do(t, http.MethodGet, "/seeker/job", nil, "")
			if out["id"].(float64) != 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		id := uint64(out["id"].(float64))
		e.do(t, http.MethodPost, "/seeker/report", seekerReport{ID: id, Attempts: 700, Seeker: "s"}, "")
		e.do(t, http.MethodPost, "/seeker/result", seekerResult{ID: id, Nonce: hex.EncodeToString(nonce[:]), Beta: hex.EncodeToString(beta), Attempts: 300, Seeker: "s"}, "")
	}()

	proof, gotNonce, attempts, found := m.mineParallel(e.pred, slot, k, 10*time.Second, nil)
	require.True(t, found)
	require.Equal(t, nonce, gotNonce)
	require.EqualValues(t, 1000, attempts)
	_, err := vrf.Verify(e.pk, txbuildercore.MineVRFMessage(e.pred, slot, nonce), proof)
	require.NoError(t, err)
	// the job is retired with the round
	_, out := e.do(t, http.MethodGet, "/seeker/job", nil, "")
	require.EqualValues(t, 0, out["id"])

	// and a round nobody solves ends at its deadline with nothing found
	start := time.Now()
	_, _, _, found = m.mineParallel(e.pred, slot+1, 60, 200*time.Millisecond, nil)
	require.False(t, found)
	require.Less(t, time.Since(start), 2*time.Second)
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }
