package streaming

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// Lifecycle of the mining transaction stream. The invariants under test are the
// ones that keep a long-running node healthy: connections are admitted up to the
// cap and refused (not evicted) beyond it, every exit path removes the
// connection, a stalled subscriber never blocks the node's event dispatch, and
// shutdown reclaims everything.

// testMiningEnv is a mining stream environment backed by a real global object
// (for logging and the shutdown context) with the event registration captured
// so the test can fire events directly.
type testMiningEnv struct {
	*global.Global
	mu       sync.Mutex
	handlers []func(*workflow.NewMiningTxEventData) bool
}

func newTestMiningEnv() *testMiningEnv {
	return &testMiningEnv{Global: global.NewDefault()}
}

func (e *testMiningEnv) OnNewMiningTx(fun func(data *workflow.NewMiningTxEventData) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers = append(e.handlers, fun)
}

// fire dispatches an event the way the node's single event-dispatch goroutine
// would: sequentially, dropping handlers that return false.
func (e *testMiningEnv) fire(data *workflow.NewMiningTxEventData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := e.handlers[:0]
	for _, h := range e.handlers {
		if h(data) {
			kept = append(kept, h)
		}
	}
	e.handlers = kept
}

// newTestServer wires a miningServer to an httptest server, bypassing viper so
// the tests do not fight over the config singleton.
func newTestServer(t *testing.T, maxConn int) (*miningServer, *testMiningEnv, *httptest.Server) {
	t.Helper()
	env := newTestMiningEnv()
	srv := &miningServer{
		miningEnvironment: env,
		maxConn:           maxConn,
		ledgerHash:        testLedgerHash,
		minerVersion:      func() uint32 { return testMinerVersion },
		banDuration:       testBanDuration,
		banned:            make(map[string]time.Time),
	}
	env.OnNewMiningTx(srv.broadcast)
	go srv.closeAllOnShutdown()

	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	t.Cleanup(func() {
		ts.Close()
		env.Stop()
	})
	return srv, env, ts
}

// the handshake: every dial of these tests presents the ledger hash and the
// miner version the server expects, the handshake test alone presents others
const (
	testLedgerHash   = "0123abcd"
	testMinerVersion = uint32(3)
	testBanDuration  = 300 * time.Millisecond
)

func wsURL(ts *httptest.Server) string {
	return wsURLWithHash(ts, testLedgerHash)
}

func wsURLWithHash(ts *httptest.Server, hash string) string {
	return wsURLWith(ts, hash, testMinerVersion)
}

func wsURLWith(ts *httptest.Server, hash string, version uint32) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "?" + MiningLedgerHashQueryKey + "=" + hash +
		"&" + MiningMinerVersionQueryKey + "=" + strconv.Itoa(int(version))
}

func dial(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testEventData(lastByte byte) *workflow.NewMiningTxEventData {
	var txid base.TransactionID
	txid[len(txid)-1] = lastByte
	return &workflow.NewMiningTxEventData{TxID: txid, TxBytes: []byte{0xDE, 0xAD, lastByte}}
}

// numConns reads the tracked connection count under the server lock.
func numConns(srv *miningServer) int {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return len(srv.conns)
}

// requireEventually polls until cond holds, so tests do not depend on the
// scheduling of the reader/writer goroutines.
func requireEventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 3*time.Second, 5*time.Millisecond, msg)
}

// a subscriber receives the raw bytes of a streamed transit, hex-encoded
func TestMiningStreamDelivers(t *testing.T) {
	srv, env, ts := newTestServer(t, 4)
	c := dial(t, ts)
	requireEventually(t, func() bool { return numConns(srv) == 1 }, "connection registered")

	env.fire(testEventData(0x07))

	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, raw, err := c.ReadMessage()
	require.NoError(t, err)

	var msg miningTxMessage
	require.NoError(t, json.Unmarshal(raw, &msg))
	require.Equal(t, hex.EncodeToString([]byte{0xDE, 0xAD, 0x07}), msg.TxBytes)
	require.NotEmpty(t, msg.TxID)
}

// every connected subscriber gets every transit
func TestMiningStreamFansOut(t *testing.T) {
	srv, env, ts := newTestServer(t, 4)
	conns := []*websocket.Conn{dial(t, ts), dial(t, ts), dial(t, ts)}
	requireEventually(t, func() bool { return numConns(srv) == 3 }, "all connections registered")

	env.fire(testEventData(0x01))

	for i, c := range conns {
		require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, raw, err := c.ReadMessage()
		require.NoErrorf(t, err, "subscriber %d", i)
		require.Contains(t, string(raw), hex.EncodeToString([]byte{0xDE, 0xAD, 0x01}))
	}
}

// at capacity a new subscriber is refused, and — critically — the miners
// already connected keep their connections rather than being evicted
func TestMiningStreamRefusesAtCapacity(t *testing.T) {
	srv, env, ts := newTestServer(t, 2)
	first, second := dial(t, ts), dial(t, ts)
	requireEventually(t, func() bool { return numConns(srv) == 2 }, "at capacity")

	// the dial itself succeeds (the upgrade completes before the cap is applied);
	// the server then closes it with a policy close frame
	third, _, err := websocket.DefaultDialer.Dial(wsURL(ts), nil)
	require.NoError(t, err)
	defer func() { _ = third.Close() }()

	require.NoError(t, third.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err = third.ReadMessage()
	require.Error(t, err)
	require.True(t, websocket.IsCloseError(err, websocket.CloseTryAgainLater),
		"expected CloseTryAgainLater, got %v", err)

	// the refused connection was never tracked
	require.Equal(t, 2, numConns(srv))

	// and both incumbents still receive
	env.fire(testEventData(0x02))
	for i, c := range []*websocket.Conn{first, second} {
		require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, _, err = c.ReadMessage()
		require.NoErrorf(t, err, "incumbent %d must survive a refused dial", i)
	}
}

// a client that goes away is reaped, and its slot is reusable
func TestMiningStreamReclaimsOnDisconnect(t *testing.T) {
	srv, _, ts := newTestServer(t, 1)
	c := dial(t, ts)
	requireEventually(t, func() bool { return numConns(srv) == 1 }, "connection registered")

	require.NoError(t, c.Close())
	requireEventually(t, func() bool { return numConns(srv) == 0 }, "connection reclaimed on disconnect")

	// the freed slot admits a new subscriber
	next := dial(t, ts)
	requireEventually(t, func() bool { return numConns(srv) == 1 }, "slot reusable")
	require.NoError(t, next.Close())
}

// A subscriber that never reads must not block the node's event dispatch, whose
// goroutine is shared by every event consumer on the node.
func TestMiningStreamStalledClientNeverBlocks(t *testing.T) {
	srv, env, ts := newTestServer(t, 2)
	_ = dial(t, ts) // dialed and never read from
	requireEventually(t, func() bool { return numConns(srv) == 1 }, "connection registered")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < miningOutQueueSize*3; i++ {
			env.fire(testEventData(byte(i)))
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("broadcast blocked on a stalled subscriber — event dispatch would stall node-wide")
	}
}

// Overflow behaviour of the outbound queue, exercised directly: a connection
// whose queue is full drops and counts rather than blocking the caller. This is
// asserted at push() level because end to end the kernel socket buffer absorbs
// the backlog long before the queue fills.
func TestMiningConnPushDropsWhenFull(t *testing.T) {
	mc := &miningConn{
		out:  make(chan []byte, 2),
		done: make(chan struct{}),
	}
	mc.push([]byte("a"))
	mc.push([]byte("b"))
	require.Zero(t, mc.dropped.Load(), "queue has room, nothing should drop")

	done := make(chan struct{})
	go func() {
		defer close(done)
		mc.push([]byte("c"))
		mc.push([]byte("d"))
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("push blocked on a full queue")
	}
	require.EqualValues(t, 2, mc.dropped.Load(), "overflow must be dropped and counted")

	// the queued messages are intact — dropping affects only the overflow
	require.Equal(t, []byte("a"), <-mc.out)
	require.Equal(t, []byte("b"), <-mc.out)
}

// node shutdown closes every subscriber
func TestMiningStreamClosesOnShutdown(t *testing.T) {
	srv, env, ts := newTestServer(t, 4)
	c := dial(t, ts)
	requireEventually(t, func() bool { return numConns(srv) == 1 }, "connection registered")

	env.Stop() // cancels the node context

	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err := c.ReadMessage()
	require.Error(t, err, "subscriber must be disconnected when the node stops")
	requireEventually(t, func() bool { return numConns(srv) == 0 }, "connections reclaimed on shutdown")
}

// close is idempotent and safe from several goroutines at once — it is called
// from the reader, the writer and the shutdown watcher
func TestMiningConnCloseIsIdempotent(t *testing.T) {
	_, _, ts := newTestServer(t, 2)
	c := dial(t, ts)
	defer func() { _ = c.Close() }()

	mc := &miningConn{
		conn: c,
		out:  make(chan []byte, 1),
		done: make(chan struct{}),
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mc.close()
		}()
	}
	wg.Wait()

	// push after close must neither block nor panic
	mc.push([]byte("x"))
}

// The handshake: a client naming another ledger, or none, is closed right
// after the upgrade with a policy-violation frame carrying the reason, and its
// address is told to stay away for the ban duration; a client with the right
// hash is admitted all the same, even from the banned address, since miners
// behind one NAT or reverse proxy share it. A refused client never occupies a
// subscriber slot.
func TestMiningStreamHandshake(t *testing.T) {
	srv, _, ts := newTestServer(t, 4)

	refused := func(url string, want string) {
		t.Helper()
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		require.NoError(t, err, "the upgrade completes; the refusal is a close frame")
		defer func() { _ = c.Close() }()
		require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, _, err = c.ReadMessage()
		var closeErr *websocket.CloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.ClosePolicyViolation, closeErr.Code)
		require.Contains(t, closeErr.Text, want)
		require.Contains(t, closeErr.Text, testLedgerHash, "the reason names the ledger hash to present")
		require.LessOrEqual(t, len(closeErr.Text), 123, "a close frame reason is at most 123 bytes")
	}

	// a stale miner names the old ledger: refused and banned
	refused(wsURLWithHash(ts, "deadbeef"), "ledger hash mismatch")
	require.Equal(t, 0, numConns(srv))

	// a retry with the wrong hash during the ban is told how long it lasts
	refused(wsURLWithHash(ts, "deadbeef"), "banned")

	// the right hash from the same address is admitted during the ban
	c := dial(t, ts)
	requireEventually(t, func() bool { return numConns(srv) == 1 }, "admitted during another client's ban")
	_ = c.Close()
	requireEventually(t, func() bool { return numConns(srv) == 0 }, "released")

	// the ban expires and a wrong hash is a fresh refusal again
	time.Sleep(2 * testBanDuration)
	refused(wsURLWithHash(ts, "deadbeef"), "refused")

	// no hash at all is a mismatch too
	refused("ws"+strings.TrimPrefix(ts.URL, "http"), "ledger hash mismatch")

	// the right ledger but another miner version is refused and told what to
	// do; a miner from before the version check presents none, which is 0
	time.Sleep(2 * testBanDuration)
	for _, version := range []uint32{testMinerVersion + 1, 0} {
		c, _, err := websocket.DefaultDialer.Dial(wsURLWith(ts, testLedgerHash, version), nil)
		require.NoError(t, err)
		require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, _, err = c.ReadMessage()
		var closeErr *websocket.CloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, websocket.ClosePolicyViolation, closeErr.Code)
		require.Contains(t, closeErr.Text, "update proxi")
		require.Contains(t, closeErr.Text, strconv.Itoa(int(testMinerVersion)), "the reason names the version to move to")
		_ = c.Close()
		require.Equal(t, 0, numConns(srv))
	}
}

// the address a ban applies to: the remote host, or the proxy's X-Real-IP when
// the request comes from loopback; a direct client cannot name another address
func TestMiningStreamClientAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:4455"
	require.Equal(t, "203.0.113.7", clientAddress(r))

	r.Header.Set("X-Real-IP", "198.51.100.9")
	require.Equal(t, "203.0.113.7", clientAddress(r), "header ignored from a non-loopback remote")

	r.RemoteAddr = "127.0.0.1:18001"
	require.Equal(t, "198.51.100.9", clientAddress(r), "header honoured behind the local proxy")
}
