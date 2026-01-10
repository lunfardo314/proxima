package streaming

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	environment interface {
		global.Logging
		OnTransaction(fun func(tx *transaction.Transaction) bool)
		OnTxDeleted(fun func(txid base.TransactionID) bool) // called whenever tx is GCed. Could be useful for the visualizer
		TxBytesStore() global.TxBytesStore
	}
	wsServer struct {
		environment
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
	srv := &wsServer{
		environment: env,
	}
	srv.Log().Infof("[%s] web socket steraming is running", TraceTag)
	http.HandleFunc(api.PathDAGVertexStream, srv.dagVertexStreamHandler)
}

func vertexDepsForTx(srv *wsServer, txidstr string) []byte {

	txid, err := base.TransactionIDFromHexString(txidstr)
	if err != nil {
		return nil
	}

	txBytesWithMetadata := srv.TxBytesStore().GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMetadata) == 0 {
		return nil
	}

	_, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		return nil
	}

	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
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

	srv.Log().Infof("[%s] web socket client connected, remote: %s", TraceTag, r.RemoteAddr)

	// Thread-safe storage for transactions per slot
	var mu sync.Mutex
	txSlots := make(map[uint32]set.Set[string]) // Slot -> Set of transaction IDs
	var latestSlot uint32

	// Goroutine to handle closing message from the client
	go func() {
		//defer wg.Done()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				srv.Log().Infof("[%s] WebSocket client disconnected, remote: %s, err: %v", TraceTag, r.RemoteAddr, err)
				_ = conn.Close() // explicitly close the connection
				return
			}

		}
	}()

	srv.OnTransaction(func(tx *transaction.Transaction) bool {
		mu.Lock()
		defer mu.Unlock()

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

		// Convert transaction to vertex
		vertexWD := api.VertexWithDependenciesFromTransaction(tx)

		// Store transaction id in its slot
		txSlots[slot].Insert(vertexWD.ID)

		// Process dependencies
		for _, i := range vertexWD.Inputs {
			txid, err := base.TransactionIDFromHexString(i)
			if err != nil {
				srv.Log().Warnf("Failed to parse TransactionID from hex: %s, err: %v", i, err)
				continue // Skip this input
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
					if err = conn.WriteMessage(websocket.TextMessage, respBin); err != nil {
						srv.Log().Infof("[%s] WebSocket client disconnected, remote: %s, err = %v", TraceTag, r.RemoteAddr, err)
						break
					}
				}
			}
		}

		// Send the transaction itself
		respBin, err := json.MarshalIndent(vertexWD, "", "  ")
		util.AssertNoError(err)

		if err = conn.WriteMessage(websocket.TextMessage, respBin); err != nil {
			srv.Log().Infof("[%s] web socket client disconnected, remote: %s, err = %v", TraceTag, r.RemoteAddr, err)
		}
		return err == nil // returns false to remove callback
	})

	srv.OnTxDeleted(func(txid base.TransactionID) bool {
		vertex := &api.VertexDelete{
			ID: txid.StringHex(),
		}
		respBin, err := json.MarshalIndent(vertex, "", "  ")
		util.AssertNoError(err)

		if err = conn.WriteMessage(websocket.TextMessage, respBin); err != nil {
			srv.Log().Infof("[%s] web socket client disconnected, remote: %s, err = %v", TraceTag, r.RemoteAddr, err)
		}
		return err == nil // returns false to remove callback
	})
}
