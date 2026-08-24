package streaming

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/spf13/viper"
)

// Mining transaction stream: every fair-launch mine-chain transit the node
// accepts is pushed to subscribed miners as raw transaction bytes.
//
// Its purpose is to remove the information asymmetry that made mining
// winner-take-all (see claude/archive/shipped/mining_tx_streaming.md). The miner that produced
// a transit knows it immediately, while everyone else used to learn of it only
// through LRB confirmation — roughly two transits later, which compounds into a
// permanent lead. Streaming levels that to one gossip hop.
//
// The node relays; it does not vouch. A mining transaction is signature-checked
// and persisted at this point but NOT constraint-validated, so its proof of work
// is unverified and the structure identifying it as a mine transit is
// attacker-forgeable. Raw bytes are streamed precisely so the miner can verify
// the mine-chain rules itself against the predecessor it already tracks. Never
// steer mining on this feed without verifying it.

const (
	// Connection defaults. A miner holds one long-lived connection, so unlike
	// the DAG visualizer stream there is no connection TTL: idle connections are
	// reaped by the ping/pong deadline instead of by age.
	defaultMaxMiningConnections = 50

	// Outbound queue per connection. Transits arrive once per mining pace (tens
	// of seconds), so this is vast headroom; it exists to absorb a stalled
	// client without ever blocking the node's event dispatch.
	miningOutQueueSize = 256

	// Keepalive. A ping every miningPingPeriod; a connection that has produced
	// neither pong nor data within miningPongWait is dropped. This is what
	// reclaims half-open connections whose peer vanished without a FIN.
	miningPingPeriod = 30 * time.Second
	miningPongWait   = 75 * time.Second

	// Clients are not expected to send anything; bound what we will read.
	miningReadLimit = 512

	miningTraceTag = "mining_stream"
)

type (
	miningEnvironment interface {
		global.Logging
		Ctx() context.Context
		OnNewMiningTx(fun func(data *workflow.NewMiningTxEventData) bool)
	}

	// miningConn is one subscribed miner. Writes happen only on its own
	// writeLoop goroutine, so the websocket is never written concurrently.
	miningConn struct {
		conn      *websocket.Conn
		remote    string
		createdAt time.Time
		out       chan []byte
		done      chan struct{}
		closeOnce sync.Once
		dropped   atomic.Uint64
	}

	miningServer struct {
		miningEnvironment
		mu      sync.Mutex
		conns   []*miningConn
		maxConn int
	}

	// miningTxMessage is one streamed transit. TxID is a convenience for logs
	// and dedup; it is derived from TxBytes and must not be trusted on its own.
	miningTxMessage struct {
		TxID    string `json:"txid"`
		TxBytes string `json:"tx_bytes"`
	}
)

// MiningConfigKey resolves a mining-stream sub-key to its node config path.
func MiningConfigKey(subKey string) string {
	return "api.mining_streaming." + subKey
}

// RunMiningTxStream installs the mining stream endpoint. Unlike the DAG
// visualizer stream it is enabled by default: miners depend on it for fair
// launch, so a node has to opt out rather than opt in.
func RunMiningTxStream(env miningEnvironment) {
	if viper.GetBool(MiningConfigKey("disable")) {
		env.Log().Infof("[%s] mining transaction streaming is disabled", miningTraceTag)
		return
	}
	maxConn := viper.GetInt(MiningConfigKey("max_connections"))
	if maxConn <= 0 {
		maxConn = defaultMaxMiningConnections
	}
	srv := &miningServer{
		miningEnvironment: env,
		maxConn:           maxConn,
	}
	// One handler for the lifetime of the node, fanning out to all connections.
	// Registering per connection would grow the listener map and repeat the
	// marshalling for every subscriber.
	env.OnNewMiningTx(srv.broadcast)

	go srv.closeAllOnShutdown()

	http.HandleFunc(api.PathMiningTxStream, srv.handler)
	env.Log().Infof("[%s] mining transaction streaming is running on %s (max connections: %d)",
		miningTraceTag, api.PathMiningTxStream, maxConn)
}

// broadcast is the event handler. It runs on the node's single event-dispatch
// goroutine, so it must never block: it marshals once and hands each connection
// a buffered, non-blocking send. Always returns true — the handler lives as long
// as the node.
func (srv *miningServer) broadcast(data *workflow.NewMiningTxEventData) bool {
	msg, err := json.Marshal(&miningTxMessage{
		TxID:    data.TxID.StringHex(),
		TxBytes: hex.EncodeToString(data.TxBytes),
	})
	if err != nil {
		srv.Log().Warnf("[%s] cannot marshal mining tx %s: %v", miningTraceTag, data.TxID.StringShort(), err)
		return true
	}

	srv.mu.Lock()
	conns := slices.Clone(srv.conns)
	srv.mu.Unlock()

	for _, c := range conns {
		c.push(msg)
	}
	srv.Tracef(miningTraceTag, "streamed %s to %d connection(s)", data.TxID.StringShort, len(conns))
	return true
}

// push enqueues without blocking. A full queue means the client is not reading;
// the message is dropped and counted, and the ping/pong deadline will eventually
// reap the connection.
func (c *miningConn) push(msg []byte) {
	select {
	case c.out <- msg:
	case <-c.done:
	default:
		c.dropped.Add(1)
	}
}

// close is the single close path, safe to call from any goroutine and any
// number of times. Closing `done` stops the writer; closing the websocket
// unblocks the reader. `out` is deliberately never closed — broadcast may still
// be sending on it.
func (c *miningConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (srv *miningServer) addConnection(c *miningConn) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	if len(srv.conns) >= srv.maxConn {
		return false
	}
	srv.conns = append(srv.conns, c)
	srv.Log().Infof("[%s] miner connected: %s (%d/%d)", miningTraceTag, c.remote, len(srv.conns), srv.maxConn)
	return true
}

func (srv *miningServer) removeConnection(c *miningConn) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	if i := slices.Index(srv.conns, c); i >= 0 {
		srv.conns = slices.Delete(srv.conns, i, i+1)
		srv.Log().Infof("[%s] miner disconnected: %s (age: %v, dropped: %d, %d/%d)",
			miningTraceTag, c.remote, time.Since(c.createdAt).Round(time.Second),
			c.dropped.Load(), len(srv.conns), srv.maxConn)
	}
}

// closeAllOnShutdown drops every subscriber when the node stops, so no
// connection outlives the process it streams from.
func (srv *miningServer) closeAllOnShutdown() {
	<-srv.Ctx().Done()

	srv.mu.Lock()
	conns := slices.Clone(srv.conns)
	srv.mu.Unlock()

	for _, c := range conns {
		c.close()
	}
}

func (srv *miningServer) handler(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: checkWebSocketOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written an error response
		srv.Log().Warnf("[%s] websocket upgrade failed, remote: %s: %v", miningTraceTag, r.RemoteAddr, err)
		return
	}

	c := &miningConn{
		conn:      conn,
		remote:    r.RemoteAddr,
		createdAt: time.Now(),
		out:       make(chan []byte, miningOutQueueSize),
		done:      make(chan struct{}),
	}

	if !srv.addConnection(c) {
		// At capacity: refuse rather than evict, so a new subscriber cannot
		// displace a miner that is working. CloseTryAgainLater tells an honest
		// client to back off and retry.
		srv.Log().Warnf("[%s] refusing %s: at capacity (%d)", miningTraceTag, r.RemoteAddr, srv.maxConn)
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "too many mining stream connections"),
			time.Now().Add(wsWriteTimeout))
		_ = conn.Close()
		return
	}
	// The handler goroutine owns the connection for its whole life: it runs the
	// writer and only returns once the connection is finished, so cleanup here
	// covers every exit path (client close, write error, read timeout, shutdown).
	defer srv.removeConnection(c)
	defer c.close()

	go c.readLoop()
	c.writeLoop()
}

// readLoop detects disconnection and keeps the read deadline fresh from pongs.
// It discards client payloads: the stream is one-directional.
func (c *miningConn) readLoop() {
	defer c.close()

	c.conn.SetReadLimit(miningReadLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(miningPongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(miningPongWait))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(miningPongWait))
	}
}

// writeLoop is the only writer on this websocket. It returns on any write
// failure, on close, or on node shutdown.
func (c *miningConn) writeLoop() {
	ticker := time.NewTicker(miningPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return

		case msg := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			if err := c.conn.WriteControl(
				websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
				return
			}
		}
	}
}
