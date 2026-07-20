package streaming

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	srv := &miningServer{miningEnvironment: env, maxConn: maxConn}
	env.OnNewMiningTx(srv.broadcast)
	go srv.closeAllOnShutdown()

	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	t.Cleanup(func() {
		ts.Close()
		env.Stop()
	})
	return srv, env, ts
}

func wsURL(ts *httptest.Server) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http")
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
