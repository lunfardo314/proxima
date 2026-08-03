package node_cmd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/proxi/glb"
)

// Subscription to the node's mining transaction stream.
//
// This is what removes the information asymmetry: without it a miner learns
// that it lost a height only when the LRB confirms a competing transit, which
// takes longer than mining one, so whoever wins once stays ahead forever. Over
// the stream a competing transit arrives within a gossip hop instead.
//
// A miner runs for days across node restarts and network glitches, so a dropped
// connection is routine and is reconnected with backoff rather than treated as
// an error. Subscribing to several nodes is supported because a single node is
// a single point of trust for liveness: it cannot forge a transit (every one is
// verified locally) but it can withhold one, which would quietly restore the
// asymmetry.

const (
	mineStreamDialTimeout  = 10 * time.Second
	mineStreamReadTimeout  = 90 * time.Second // must exceed the server's 30s ping period
	mineStreamRetryBase    = time.Second
	mineStreamRetryMax     = 30 * time.Second
	mineStreamMaxMsgSize   = 1 << 20
	mineStreamReportPeriod = 5 * time.Minute
)

// streamMessage mirrors the server's mining stream frame.
type streamMessage struct {
	TxID    string `json:"txid"`
	TxBytes string `json:"tx_bytes"`
}

// miningStreamURL converts a node API endpoint into the mining stream URL.
func miningStreamURL(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("bad endpoint %q: %w", endpoint, err)
	}
	switch u.Scheme {
	case "http", "":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("bad endpoint scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("bad endpoint %q: no host", endpoint)
	}
	u.Path = api.PathMiningTxStream
	u.RawQuery = ""
	return u.String(), nil
}

// runStreams subscribes to every endpoint until the context is cancelled. Each
// endpoint gets its own goroutine; duplicates across endpoints are harmless
// because the tree ignores transits it already holds.
func (m *miner) runStreams(ctx context.Context, endpoints []string) {
	for _, ep := range endpoints {
		streamURL, err := miningStreamURL(ep)
		if err != nil {
			glb.Infof("mining stream: %v", err)
			continue
		}
		go m.streamLoop(ctx, streamURL)
	}
}

// streamLoop keeps one subscription alive, reconnecting with backoff.
func (m *miner) streamLoop(ctx context.Context, streamURL string) {
	delay := mineStreamRetryBase
	for ctx.Err() == nil {
		connectedAt := time.Now()
		err := m.streamOnce(ctx, streamURL)
		if ctx.Err() != nil {
			return
		}
		// a connection that lasted a while is a healthy one that dropped;
		// restart its backoff so a long-lived link reconnects promptly
		if time.Since(connectedAt) > mineStreamRetryMax {
			delay = mineStreamRetryBase
		}
		glb.Verbosef("   mining stream %s disconnected (%v); reconnecting in %v", streamURL, err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > mineStreamRetryMax {
			delay = mineStreamRetryMax
		}
	}
}

// streamOnce holds one connection until it fails.
func (m *miner) streamOnce(ctx context.Context, streamURL string) error {
	dialer := websocket.Dialer{HandshakeTimeout: mineStreamDialTimeout}
	conn, _, err := dialer.DialContext(ctx, streamURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	glb.Infof("mining stream connected: %s", streamURL)
	conn.SetReadLimit(mineStreamMaxMsgSize)

	// close the connection when the miner stops, so the read below unblocks
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	// The server pings; every ping refreshes the deadline, so a silent stream
	// is distinguished from a dead one.
	_ = conn.SetReadDeadline(time.Now().Add(mineStreamReadTimeout))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(mineStreamReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(mineStreamReadTimeout))

		var msg streamMessage
		if err = json.Unmarshal(raw, &msg); err != nil {
			glb.Verbosef("   mining stream: bad frame: %v", err)
			continue
		}
		txBytes, err := hex.DecodeString(msg.TxBytes)
		if err != nil {
			glb.Verbosef("   mining stream: bad tx_bytes: %v", err)
			continue
		}
		m.onStreamedTransit(txBytes)
	}
}

// onStreamedTransit verifies a streamed transit against the predecessor it
// spends and folds it into the tree. A transit whose predecessor is not known
// yet is parked and retried once that predecessor arrives — stream frames can
// overtake one another, and a miner that just started knows only the root.
func (m *miner) onStreamedTransit(txBytes []byte) {
	parent, err := transitParent(txBytes)
	if err != nil {
		glb.Verbosef("   mining stream: %v", err)
		return
	}
	pred := m.tree.tipFor(parent)
	if pred == nil {
		m.tree.addPending(parent, txBytes)
		return
	}
	m.acceptTransit(pred, txBytes, false)
}

// acceptTransit verifies one transit against a known predecessor, inserts it,
// and then releases anything that was waiting on the tip it produces.
func (m *miner) acceptTransit(pred *mineTip, txBytes []byte, own bool) {
	succ, err := verifyMineTransit(m.lib, m.consts, pred, txBytes)
	if err != nil {
		// Expected in normal operation: competing transits at a height we have
		// already moved past, and stale frames after a re-anchor. Also where a
		// forged transit lands.
		glb.Verbosef("   mining stream: rejected transit: %v", err)
		return
	}
	txid := succ.oid.TransactionID()
	if !m.tree.insert(txid, pred.oid, succ, own) {
		return
	}
	glb.Verbosef("   transit #%d %s accepted%s", succ.cc.TransitionCounter, txid.StringShort(), ownSuffix(own))

	if m.tree.superseded() {
		m.abort.Store(true)
	}
	// a transit that was waiting for this one can now be verified
	for _, p := range m.tree.takePending(succ.oid) {
		m.acceptTransit(succ, p.txBytes, false)
	}
}

func ownSuffix(own bool) string {
	if own {
		return " (own)"
	}
	return ""
}
