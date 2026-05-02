package streaming

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
)

const (
	wsWriteTimeout        = 5 * time.Second
	defaultMaxConnections = 5
	defaultConnectionTTL  = 5 // minutes
)

type (
	environment interface {
		global.Logging
		OnNewVertex(fun func(data *workflow.NewVertexEventData) bool)
		OnTxDeleted(fun func(txid base.TransactionID) bool)
		TxBytesStore() global.TxBytesStore
	}
	// wsConnection tracks a single WebSocket client connection
	wsConnection struct {
		conn      *websocket.Conn
		remote    string
		createdAt time.Time
		closed    atomic.Bool
	}

	wsServer struct {
		environment
		mu          sync.Mutex
		connections []*wsConnection
		maxConn     int
		connTTL     time.Duration
	}
)

const TraceTag = "streaming"

// checkWebSocketOrigin validates the WebSocket connection origin.
// Returns true if no Origin header (same-origin request) or if Origin matches the Host.
// This prevents cross-site WebSocket hijacking attacks.
func checkWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin header means same-origin request
		return true
	}
	// Allow if origin matches the host (handles both http and https)
	host := r.Host
	return origin == "http://"+host || origin == "https://"+host
}

func Run(env environment) {
	maxConn := viper.GetInt("streaming.max_connections")
	if maxConn <= 0 {
		maxConn = defaultMaxConnections
	}
	connTTLMinutes := viper.GetInt("streaming.connection_ttl_minutes")
	if connTTLMinutes <= 0 {
		connTTLMinutes = defaultConnectionTTL
	}
	srv := &wsServer{
		environment: env,
		maxConn:     maxConn,
		connTTL:     time.Duration(connTTLMinutes) * time.Minute,
	}
	srv.Log().Infof("[%s] web socket streaming is running (max connections: %d, TTL: %dm)",
		TraceTag, maxConn, connTTLMinutes)
	http.HandleFunc(api.PathDAGVertexStream, srv.dagVertexStreamHandler)
}

// addConnection registers a new connection, evicting the oldest if at capacity.
func (srv *wsServer) addConnection(conn *websocket.Conn, remote string) *wsConnection {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	// evict expired connections first
	now := time.Now()
	remaining := srv.connections[:0]
	for _, c := range srv.connections {
		if now.Sub(c.createdAt) > srv.connTTL {
			srv.Log().Infof("[%s] closing expired connection %s (age: %v)",
				TraceTag, c.remote, now.Sub(c.createdAt).Round(time.Second))
			c.closed.Store(true)
			_ = c.conn.Close()
		} else {
			remaining = append(remaining, c)
		}
	}
	srv.connections = remaining

	// if still at capacity, evict the oldest
	if len(srv.connections) >= srv.maxConn {
		oldest := srv.connections[0]
		srv.Log().Infof("[%s] evicting oldest connection %s (age: %v) to make room",
			TraceTag, oldest.remote, time.Since(oldest.createdAt).Round(time.Second))
		oldest.closed.Store(true)
		_ = oldest.conn.Close()
		srv.connections = srv.connections[1:]
	}

	wsc := &wsConnection{
		conn:      conn,
		remote:    remote,
		createdAt: time.Now(),
	}
	srv.connections = append(srv.connections, wsc)
	srv.Log().Infof("[%s] connection added: %s (%d/%d)", TraceTag, remote, len(srv.connections), srv.maxConn)
	return wsc
}

// removeConnection removes a connection from the tracked list.
func (srv *wsServer) removeConnection(wsc *wsConnection) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	for i, c := range srv.connections {
		if c == wsc {
			srv.connections = append(srv.connections[:i], srv.connections[i+1:]...)
			break
		}
	}
}

func vertexDepsForTx(srv *wsServer, txidstr string) []byte {

	txid, err := base.TransactionIDFromHexString(txidstr)
	if err != nil {
		return nil
	}

	txBytes := srv.TxBytesStore().GetTxBytes(&txid)
	if len(txBytes) == 0 {
		return nil
	}

	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return nil
	}
	resp := api.VertexWithDependenciesFromTransaction(tx)
	respBin, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return respBin
}

// WebSocket handler
const keepMaxSlots = 10 // Keep only last 10 slots

func (srv *wsServer) dagVertexStreamHandler(w http.ResponseWriter, r *http.Request) {
	u := websocket.Upgrader{CheckOrigin: checkWebSocketOrigin}
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		srv.Log().Warnf("[%s] WebSocket upgrade failed, remote: %s", TraceTag, r.RemoteAddr)
		api.WriteErr(w, "failed to upgrade to websocket connection")
		return
	}

	// register connection with cap enforcement and TTL tracking
	wsc := srv.addConnection(conn, r.RemoteAddr)

	// writeMsg sets a write deadline before each write so a slow/dead client
	// cannot block the events consumer goroutine indefinitely.
	writeMsg := func(data []byte) error {
		if wsc.closed.Load() {
			return websocket.ErrCloseSent
		}
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	// Thread-safe storage for transactions per slot
	var mu sync.Mutex
	txSlots := make(map[uint32]set.Set[string]) // Slot -> Set of transaction IDs
	var latestSlot uint32

	// Goroutine to handle closing message from the client
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				srv.Log().Infof("[%s] WebSocket client disconnected, remote: %s, err: %v", TraceTag, r.RemoteAddr, err)
				wsc.closed.Store(true)
				_ = conn.Close()
				srv.removeConnection(wsc)
				return
			}
		}
	}()

	// TTL watchdog: close connection after configured timeout
	go func() {
		timer := time.NewTimer(srv.connTTL)
		defer timer.Stop()
		<-timer.C
		if !wsc.closed.Load() {
			srv.Log().Infof("[%s] connection TTL expired, disconnecting %s", TraceTag, r.RemoteAddr)
			wsc.closed.Store(true)
			_ = conn.Close()
			srv.removeConnection(wsc)
		}
	}()

	srv.OnNewVertex(func(data *workflow.NewVertexEventData) bool {
		if wsc.closed.Load() {
			return false
		}

		mu.Lock()
		defer mu.Unlock()

		tx := data.Transaction
		txID := tx.IDShortString()
		slot := tx.Timestamp().Slot

		srv.Tracef(TraceTag, "Processing TX id: %s (Slot: %d)", txID, slot)

		// Initialize latestSlot dynamically
		if latestSlot == 0 {
			latestSlot = slot
		}

		// Cleanup old slots
		if slot > latestSlot {
			latestSlot = slot
			for oldSlot := range txSlots {
				if oldSlot < latestSlot-keepMaxSlots {
					delete(txSlots, oldSlot)
					srv.Tracef(TraceTag, "Removed old slot: %d", oldSlot)
				}
			}
		}

		// Ensure slot set exists
		if _, exists := txSlots[slot]; !exists {
			txSlots[slot] = set.New[string]()
		}

		// CoverageDelta / Supply used to come from event data's persistent
		// metadata; after metadata-refactor §7 they live on the produced
		// stem (branch txs only). VertexWithDependenciesExtended now skips
		// them — Phase F can wire them back from the stem if needed.
		vertexWD := api.VertexWithDependenciesExtended(
			tx,
			nil,
			nil,
			data.SeqName,
		)

		// Store transaction id in its slot
		txSlots[slot].Insert(vertexWD.ID)

		// Process dependencies
		for _, i := range vertexWD.Inputs {
			txid, err := base.TransactionIDFromHexString(i)
			if err != nil {
				srv.Log().Warnf("Failed to parse TransactionID from hex: %s, err: %v", i, err)
				continue
			}

			depSlot := txid.Timestamp().Slot

			// Ensure slot set exists
			if _, exists := txSlots[depSlot]; !exists {
				txSlots[depSlot] = set.New[string]()
			}

			if !txSlots[depSlot].Contains(i) {
				respBin := vertexDepsForTx(srv, i)
				if respBin != nil {
					srv.Tracef(TraceTag, "Send tx not seen yet %s", i)
					txSlots[depSlot].Insert(i)
					if err = writeMsg(respBin); err != nil {
						srv.Log().Infof("[%s] WebSocket client disconnected, remote: %s, err = %v", TraceTag, r.RemoteAddr, err)
						wsc.closed.Store(true)
						_ = conn.Close()
						break
					}
				}
			}
		}

		if wsc.closed.Load() {
			return false
		}

		// Send the transaction itself
		respBin, err := json.MarshalIndent(vertexWD, "", "  ")
		util.AssertNoError(err)

		if err = writeMsg(respBin); err != nil {
			srv.Log().Infof("[%s] web socket client disconnected, remote: %s, err = %v", TraceTag, r.RemoteAddr, err)
			wsc.closed.Store(true)
			_ = conn.Close()
			srv.removeConnection(wsc)
		}
		return !wsc.closed.Load()
	})

	srv.OnTxDeleted(func(txid base.TransactionID) bool {
		if wsc.closed.Load() {
			return false
		}
		vertex := &api.VertexDelete{
			ID: txid.StringHex(),
		}
		respBin, err := json.MarshalIndent(vertex, "", "  ")
		util.AssertNoError(err)

		if err = writeMsg(respBin); err != nil {
			srv.Log().Infof("[%s] web socket client disconnected, remote: %s, err = %v", TraceTag, r.RemoteAddr, err)
			wsc.closed.Store(true)
			_ = conn.Close()
			srv.removeConnection(wsc)
		}
		return !wsc.closed.Load()
	})
}
