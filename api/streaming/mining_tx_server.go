package streaming

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/spf13/viper"
)

// Mining transaction stream: every fair-launch mine-chain transit the node
// accepts is pushed to subscribed miners as raw transaction bytes.
//
// Its purpose is to remove the information asymmetry that made mining
// winner-take-all (see kb/archive/shipped/mining_tx_streaming.md). The miner that produced
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

	// Handshake: the client names the ledger it mines on, the library hash at
	// slot 0, in the query of the upgrade request. A client on another ledger,
	// typically a miner left running from a stopped network, is closed right
	// after the upgrade with the reason and its address is noted for
	// miningBanDuration, so its retries cost the node one handshake and no log
	// line, and never a subscriber slot. The ban is not extended by retries, or
	// a client retrying on a timer would never get out of it, and it never
	// touches a client presenting the right hash.
	MiningLedgerHashQueryKey = "ledger_hash"
	// The client also names its miner version, which must equal the ledger's
	// constMinerVersion for the current slot: that constant is bumped when
	// every miner must move to a new release, and the stream refuses the old
	// ones with the reason, closing already subscribed ones at the bump. A
	// missing version is version 0, so a miner from before the check is
	// refused too.
	MiningMinerVersionQueryKey = "miner_version"
	miningBanDuration          = 5 * time.Minute
	miningBanMaxEntries        = 10_000

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
		version   uint32 // miner version presented at the handshake
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
		// ledgerHash is the hex library hash at slot 0 a client must present
		ledgerHash string
		// minerVersion is the reference miner version the ledger expects now
		minerVersion func() uint32
		banDuration  time.Duration
		banned      map[string]time.Time // client address -> refused until
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
func RunMiningTxStream(env miningEnvironment, mux *http.ServeMux) {
	if viper.GetBool(MiningConfigKey("disable")) {
		env.Log().Infof("[%s] mining transaction streaming is disabled", miningTraceTag)
		return
	}
	maxConn := viper.GetInt(MiningConfigKey("max_connections"))
	if maxConn <= 0 {
		maxConn = defaultMaxMiningConnections
	}
	ledgerHash := ledger.L(0).Library.LibraryHash()
	srv := &miningServer{
		miningEnvironment: env,
		maxConn:           maxConn,
		ledgerHash:        hex.EncodeToString(ledgerHash[:]),
		minerVersion:      func() uint32 { return ledger.L(ledger.TimeNow().Slot).MinerVersion },
		banDuration:       miningBanDuration,
		banned:            make(map[string]time.Time),
	}
	// One handler for the lifetime of the node, fanning out to all connections.
	// Registering per connection would grow the listener map and repeat the
	// marshalling for every subscriber.
	env.OnNewMiningTx(srv.broadcast)

	go srv.closeAllOnShutdown()

	mux.HandleFunc(api.PathMiningTxStream, srv.handler)
	env.Log().Infof("[%s] mining transaction streaming is running on %s (max connections: %d, clients must present ledger hash %s and miner version %d)",
		miningTraceTag, api.PathMiningTxStream, maxConn, srv.ledgerHash, srv.minerVersion())
}

// clientAddress is the address the ban applies to: the host of the remote
// address, or X-Real-IP when the request comes from a reverse proxy on this
// machine. The header is trusted from loopback only, or a direct client could
// have any address it names refused.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return real
		}
	}
	return host
}

// admit applies the handshake to an upgraded connection and returns the miner
// version it presented. A client presenting the ledger hash and the expected
// miner version is always admitted: the ban is for the address a refused client
// came from, so that its retries are answered without a log line and told to
// stay away, and it must never reach a correct client sharing that address, as
// miners behind one NAT or one reverse proxy do. A wrong or missing hash, or a
// miner version other than the ledger's, gets a close frame with code 1008 and
// the reason, the first time with a warning and the ban, during the ban with the
// time left. The handshake is judged after the upgrade, not on the HTTP request,
// because a websocket client shows the text of a close frame and discards the
// body of a refused upgrade: this is the only form in which a miner built for
// another ledger or version, left running, can display why it is refused.
// Returns false when the connection was closed.
func (srv *miningServer) admit(conn *websocket.Conn, r *http.Request) (uint32, bool) {
	gotHash := r.URL.Query().Get(MiningLedgerHashQueryKey)
	version, _ := strconv.ParseUint(r.URL.Query().Get(MiningMinerVersionQueryKey), 10, 32)
	required := srv.minerVersion()
	var reason string
	switch {
	case gotHash != srv.ledgerHash:
		reason = "ledger hash mismatch, this ledger: " + srv.ledgerHash
	case uint32(version) != required:
		reason = minerVersionReason(uint32(version), required)
	default:
		return uint32(version), true
	}
	addr := clientAddress(r)

	srv.mu.Lock()
	defer srv.mu.Unlock()

	now := time.Now()
	if until, ok := srv.banned[addr]; ok && now.Before(until) {
		srv.Tracef(miningTraceTag, "refusing %s, banned for %v more", addr, until.Sub(now).Round(time.Second))
		closeRefused(conn, fmt.Sprintf("banned %v more, %s", until.Sub(now).Round(time.Second), reason))
		return 0, false
	}
	// sweep expired bans when adding, so the map is bounded by the ban duration;
	// at the entry cap refuse without remembering, which only costs the
	// offender's next request a handshake
	for a, until := range srv.banned {
		if !now.Before(until) {
			delete(srv.banned, a)
		}
	}
	if len(srv.banned) < miningBanMaxEntries {
		srv.banned[addr] = now.Add(srv.banDuration)
	}
	srv.Log().Warnf("[%s] refusing %s for %v: ledger hash %q, miner version %d; this ledger's are %s and %d; the miner is on another ledger or an older version",
		miningTraceTag, addr, srv.banDuration, gotHash, version, srv.ledgerHash, required)
	closeRefused(conn, fmt.Sprintf("%s, refused %v", reason, srv.banDuration))
	return 0, false
}

// minerVersionReason is the close reason of a miner on another version; it
// is what an old miner shows, so it says what to do.
func minerVersionReason(got, required uint32) string {
	return fmt.Sprintf("miner version %d, this ledger requires %d: update proxi", got, required)
}

// closeRefused sends a policy-violation close frame carrying the reason, which
// a close frame limits to 123 bytes, and closes the connection.
func closeRefused(conn *websocket.Conn, reason string) {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason),
		time.Now().Add(wsWriteTimeout))
	_ = conn.Close()
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
	version, ok := srv.admit(conn, r)
	if !ok {
		return
	}

	c := &miningConn{
		conn:      conn,
		remote:    clientAddress(r),
		version:   version,
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
	c.writeLoop(srv)
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
// failure, on close, on node shutdown, or when the ledger's miner version
// moves past the one this subscriber presented.
func (c *miningConn) writeLoop(srv *miningServer) {
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
			// the ledger's miner version can change at a slot: a subscriber
			// on the old one is told so and dropped, instead of streaming to
			// a miner whose every transit is now invalid
			if required := srv.minerVersion(); c.version != required {
				srv.Log().Infof("[%s] closing %s: %s", miningTraceTag, c.remote, minerVersionReason(c.version, required))
				closeRefused(c.conn, minerVersionReason(c.version, required))
				return
			}
			if err := c.conn.WriteControl(
				websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
				return
			}
		}
	}
}
