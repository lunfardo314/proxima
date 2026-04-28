// Package dag_explorer serves a static DAG explorer that browses the txstore
// database. It is mounted both into the running node's API server and into the
// `proxi db txstore dagviz` standalone HTTP server. The two share the same
// handlers, paths and HTML page; the only difference is which TxStore they
// read from.
//
// Not to be confused with package dagviz, which visualizes the live MemDAG of
// a running node.
package dag_explorer

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

//go:embed dag_explorer.html
var dagExplorerHTML []byte

// TxStore is the read interface the DAG explorer needs over the txstore DB:
// per-txid lookups (TxBytesGet) plus prefix iteration so we can walk all
// transactions in a given slot. Satisfied by *txstore.SimpleTxBytesStore.
type TxStore interface {
	global.TxBytesGet
	Iterator(prefix []byte) common.KVIterator
}

// Register wires all DAG explorer routes (HTML page + JSON APIs) into the
// supplied addHandler. Used by both the node API server and the proxi
// standalone HTTP server.
func Register(addHandler func(string, func(http.ResponseWriter, *http.Request)), store TxStore) {
	addHandler(api.PathDAGExplorer, servePage)
	addHandler(api.PathDAGExplorerPastCone, func(w http.ResponseWriter, r *http.Request) { servePastCone(w, r, store) })
	addHandler(api.PathDAGExplorerSlot, func(w http.ResponseWriter, r *http.Request) { serveSlot(w, r, store) })
	addHandler(api.PathDAGExplorerFindTx, func(w http.ResponseWriter, r *http.Request) { serveFindTx(w, r, store) })
	addHandler(api.PathDAGExplorerTxDetail, func(w http.ResponseWriter, r *http.Request) { serveTxDetail(w, r, store) })
}

func servePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dagExplorerHTML)
}

// JSON response types

type vertex struct {
	ID             string  `json:"id"`
	ShortID        string  `json:"short_id"`
	Slot           uint32  `json:"slot"`
	Tick           byte    `json:"tick"`
	IsSequencer    bool    `json:"is_seq"`
	IsBranch       bool    `json:"is_branch"`
	SeqChainID     string  `json:"seq_chain_id,omitempty"`
	NumInputs      int     `json:"num_inputs"`
	NumOutputs     int     `json:"num_outputs"`
	LedgerCoverage *uint64 `json:"ledger_coverage,omitempty"`
	CoverageDelta  *uint64 `json:"coverage_delta,omitempty"`
	Supply         *uint64 `json:"supply,omitempty"`
	SlotInflation  *uint64 `json:"slot_inflation,omitempty"`
	IsTip          bool    `json:"is_tip,omitempty"`
	IsLeaf         bool    `json:"is_leaf,omitempty"`
	IsMissing      bool    `json:"is_missing,omitempty"`
}

type edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Type  string `json:"type"` // "input", "endorsement", "baseline"
	Label string `json:"label,omitempty"`
}

type graph struct {
	Vertices []vertex `json:"vertices"`
	Edges    []edge   `json:"edges"`
	TipID    string   `json:"tip_id,omitempty"`
}

// loader collects vertices and edges from the txstore

type loader struct {
	store   TxStore
	txCache map[base.TransactionID]*transaction.Transaction
	visited map[base.TransactionID]bool
	data    graph
}

func (l *loader) load(txid base.TransactionID, depth int, isTip bool) {
	if l.visited[txid] {
		return
	}
	l.visited[txid] = true

	txBytesWithMeta := l.store.GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMeta) == 0 {
		l.data.Vertices = append(l.data.Vertices, vertex{
			ID:        hex.EncodeToString(txid.Bytes()),
			ShortID:   txid.StringShort(),
			Slot:      txid.Slot(),
			Tick:      txid.Tick(),
			IsMissing: true,
		})
		return
	}

	txBytes, meta, err := txmetadata.ParseTxMetadata(txBytesWithMeta)
	if err != nil {
		return
	}
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return
	}
	l.txCache[txid] = tx

	v := vertex{
		ID:          hex.EncodeToString(txid.Bytes()),
		ShortID:     txid.StringShort(),
		Slot:        txid.Slot(),
		Tick:        txid.Tick(),
		IsSequencer: txid.IsSequencerTransaction(),
		IsBranch:    txid.IsBranchTransaction(),
		NumInputs:   tx.NumInputs(),
		NumOutputs:  tx.NumProducedOutputs(),
		IsTip:       isTip,
		IsLeaf:      depth <= 0,
	}
	if txid.IsSequencerTransaction() {
		if seqData := tx.SequencerTransactionData(); seqData != nil {
			v.SeqChainID = seqData.SequencerID.StringShort()
		}
	}
	if meta != nil {
		v.LedgerCoverage = meta.LedgerCoverage
		v.CoverageDelta = meta.CoverageDelta
		v.Supply = meta.Supply
		v.SlotInflation = meta.SlotInflation
	}
	l.data.Vertices = append(l.data.Vertices, v)

	if depth <= 0 {
		return
	}

	l.addEdges(txid, tx, depth)
}

func (l *loader) addEdges(txid base.TransactionID, tx *transaction.Transaction, depth int) {
	txHex := hex.EncodeToString(txid.Bytes())

	// load inputs
	for i := 0; i < tx.NumInputs(); i++ {
		oid := tx.MustInputAt(byte(i))
		inpTxID := oid.TransactionID()
		l.load(inpTxID, depth-1, false)

		label := fmt.Sprintf("#%d", oid.Index())
		if inpTx, ok := l.txCache[inpTxID]; ok {
			if out, err := inpTx.ProducedOutputAt(oid.Index()); err == nil {
				label = fmt.Sprintf("%s(#%d)", util.Th(out.TokenBalance()), oid.Index())
			}
		}
		l.data.Edges = append(l.data.Edges, edge{
			From:  txHex,
			To:    hex.EncodeToString(inpTxID.Bytes()),
			Type:  "input",
			Label: label,
		})
	}

	// load endorsements
	for i := 0; i < tx.NumEndorsements(); i++ {
		endID := tx.MustEndorsementAt(byte(i))
		l.load(endID, depth-1, false)
		l.data.Edges = append(l.data.Edges, edge{
			From: txHex,
			To:   hex.EncodeToString(endID.Bytes()),
			Type: "endorsement",
		})
	}

	// explicit baseline
	if baselineID, ok := tx.ExplicitBaseline(); ok {
		l.load(baselineID, depth-1, false)
		l.data.Edges = append(l.data.Edges, edge{
			From: txHex,
			To:   hex.EncodeToString(baselineID.Bytes()),
			Type: "baseline",
		})
	}
}

func servePastCone(w http.ResponseWriter, r *http.Request, store TxStore) {
	txidHex := r.URL.Query().Get("txid")
	depthStr := r.URL.Query().Get("depth")
	if txidHex == "" {
		http.Error(w, "missing txid parameter", http.StatusBadRequest)
		return
	}
	depth := 6
	if depthStr != "" {
		if d, err := strconv.Atoi(depthStr); err == nil && d >= 1 {
			depth = d
		}
	}
	txid, err := base.TransactionIDFromHexString(txidHex)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid txid: %v", err), http.StatusBadRequest)
		return
	}

	l := &loader{
		store:   store,
		txCache: make(map[base.TransactionID]*transaction.Transaction),
		visited: make(map[base.TransactionID]bool),
	}
	l.load(txid, depth, true)
	l.data.TipID = hex.EncodeToString(txid.Bytes())

	sortVertices(l.data.Vertices)
	ensureNonNil(&l.data)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(l.data)
}

// serveSlot returns all transactions in the given slot and optionally several slots back.
// Transactions within the range are loaded at depth 1 (edges to immediate dependencies),
// while dependencies outside the range are loaded at depth 0 (vertex only, no further edges).
func serveSlot(w http.ResponseWriter, r *http.Request, store TxStore) {
	slotStr := r.URL.Query().Get("slot")
	if slotStr == "" {
		http.Error(w, "missing slot parameter", http.StatusBadRequest)
		return
	}
	slot64, err := strconv.ParseUint(slotStr, 10, 32)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid slot: %v", err), http.StatusBadRequest)
		return
	}
	slot := uint32(slot64)

	slotsBack := 0
	if sb := r.URL.Query().Get("slots_back"); sb != "" {
		if n, err := strconv.Atoi(sb); err == nil && n >= 0 {
			slotsBack = n
		}
	}

	l := &loader{
		store:   store,
		txCache: make(map[base.TransactionID]*transaction.Transaction),
		visited: make(map[base.TransactionID]bool),
	}

	// collect and load all txids in the slot range
	firstSlot := slot
	if uint32(slotsBack) < slot {
		firstSlot = slot - uint32(slotsBack)
	} else {
		firstSlot = 0
	}
	for s := firstSlot; s <= slot; s++ {
		prefix := base.Slot2Bytes(s)
		store.Iterator(prefix).IterateKeys(func(k []byte) bool {
			txid, err := base.TransactionIDFromBytes(k)
			if err != nil {
				return true
			}
			// load with depth 1: creates the vertex AND its edges (dependencies loaded at depth 0)
			l.load(txid, 1, false)
			return true
		})
	}

	sortVertices(l.data.Vertices)
	ensureNonNil(&l.data)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(l.data)
}

// serveFindTx searches for transactions matching a prefix.
// Accepts short format like "220942|36" or "220942|36sq]0066f7" or plain hex prefix.
func serveFindTx(w http.ResponseWriter, r *http.Request, store TxStore) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}

	type findResult struct {
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
	}

	var results []findResult

	// try to parse short format: [slot|tickTYPE]hash.. or slot|tick
	if slot, tick, hashPrefix, ok := parseShortTxID(q); ok {
		prefix := base.Slot2Bytes(slot)
		store.Iterator(prefix).IterateKeys(func(k []byte) bool {
			txid, err := base.TransactionIDFromBytes(k)
			if err != nil {
				return true
			}
			// match tick if specified (tick=255 means any tick)
			if tick != 255 && txid.Tick() != tick {
				return true
			}
			// match hash prefix
			if hashPrefix != "" {
				txHex := hex.EncodeToString(txid.Bytes())
				// hash starts after the 5 timestamp bytes = 10 hex chars
				if len(txHex) > 10 && !strings.HasPrefix(txHex[10:], hashPrefix) {
					return true
				}
			}
			results = append(results, findResult{
				ID:      hex.EncodeToString(txid.Bytes()),
				ShortID: txid.StringShort(),
			})
			return len(results) < 50
		})
	} else {
		// try as hex prefix: iterate all keys that start with decoded hex prefix
		prefixBytes, err := hex.DecodeString(q)
		if err != nil || len(prefixBytes) == 0 {
			http.Error(w, "cannot parse query: use slot|tick, [slot|tick]hash, or hex prefix", http.StatusBadRequest)
			return
		}
		store.Iterator(prefixBytes).IterateKeys(func(k []byte) bool {
			if !hasPrefix(k, prefixBytes) {
				return false // stop iteration, past the prefix
			}
			txid, err := base.TransactionIDFromBytes(k)
			if err != nil {
				return true
			}
			results = append(results, findResult{
				ID:      hex.EncodeToString(txid.Bytes()),
				ShortID: txid.StringShort(),
			})
			return len(results) < 50
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

// serveTxDetail returns parsed transaction text (same as `proxi db txstore get -p`)
func serveTxDetail(w http.ResponseWriter, r *http.Request, store TxStore) {
	txidHex := r.URL.Query().Get("txid")
	if txidHex == "" {
		http.Error(w, "missing txid parameter", http.StatusBadRequest)
		return
	}
	txid, err := base.TransactionIDFromHexString(txidHex)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid txid: %v", err), http.StatusBadRequest)
		return
	}

	txBytesWithMeta := store.GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMeta) == 0 {
		http.Error(w, "transaction not found", http.StatusNotFound)
		return
	}

	metaBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMeta)
	if err != nil {
		http.Error(w, fmt.Sprintf("metadata split error: %v", err), http.StatusInternalServerError)
		return
	}

	meta, err := txmetadata.TransactionMetadataFromBytes(metaBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf("metadata parse error: %v", err), http.StatusInternalServerError)
		return
	}

	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf("tx parse error: %v", err), http.StatusInternalServerError)
		return
	}

	_ = tx.SetFullContext(func(i byte) (*ledger.Output, error) {
		return txstore.LoadOutput(store, tx.MustInputAt(i))
	})

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "--- transaction ---\n%s\n--- metadata ---\n%s", tx.String(), meta.String())
}

// parseShortTxID parses formats like:
//
//	"220942|36" → slot=220942, tick=36, hashPrefix=""
//	"220942|36sq]0066f7" → slot=220942, tick=36, hashPrefix="0066f7"
//	"[220942|36sq]0066f7" → same with leading bracket stripped
//
// Returns tick=255 if no tick specified. Returns ok=false if not parseable.
func parseShortTxID(s string) (slot uint32, tick byte, hashPrefix string, ok bool) {
	s = strings.TrimPrefix(s, "[")

	// find the pipe separator
	pipeIdx := strings.Index(s, "|")
	if pipeIdx < 1 {
		// try plain number as slot
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return 0, 0, "", false
		}
		return uint32(n), 255, "", true
	}

	// parse slot
	slotN, err := strconv.ParseUint(s[:pipeIdx], 10, 32)
	if err != nil {
		return 0, 0, "", false
	}
	slot = uint32(slotN)

	rest := s[pipeIdx+1:]

	// extract tick: digits at the start
	tickEnd := 0
	for tickEnd < len(rest) && rest[tickEnd] >= '0' && rest[tickEnd] <= '9' {
		tickEnd++
	}
	if tickEnd == 0 {
		return slot, 255, "", true
	}
	tickN, err := strconv.ParseUint(rest[:tickEnd], 10, 8)
	if err != nil {
		return slot, 255, "", true
	}
	tick = byte(tickN)
	rest = rest[tickEnd:]

	// skip type suffix like "sq]" or "br]" or "]"
	if idx := strings.Index(rest, "]"); idx >= 0 {
		rest = rest[idx+1:]
	} else {
		rest = ""
	}

	// remaining is hash prefix (hex chars), strip trailing ".."
	hashPrefix = strings.TrimRight(strings.TrimSpace(rest), ".")
	return slot, tick, hashPrefix, true
}

func hasPrefix(data, prefix []byte) bool {
	if len(data) < len(prefix) {
		return false
	}
	for i, b := range prefix {
		if data[i] != b {
			return false
		}
	}
	return true
}

// ensureNonNil prevents nil slices from being serialized as JSON null
func ensureNonNil(d *graph) {
	if d.Vertices == nil {
		d.Vertices = []vertex{}
	}
	if d.Edges == nil {
		d.Edges = []edge{}
	}
}

func sortVertices(vertices []vertex) {
	sort.Slice(vertices, func(i, j int) bool {
		vi, vj := vertices[i], vertices[j]
		if vi.Slot != vj.Slot {
			return vi.Slot < vj.Slot
		}
		return vi.Tick < vj.Tick
	})
}
