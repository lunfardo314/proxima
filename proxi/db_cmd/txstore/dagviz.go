package txstore

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initDagVizCmd() *cobra.Command {
	dagVizCmd := &cobra.Command{
		Use:   "dagviz [--port 8080]",
		Short: "starts interactive DAG explorer in browser, reading from txstore",
		Args:  cobra.NoArgs,
		Run:   runDagVizCmd,
	}
	dagVizCmd.Flags().IntP("port", "p", 8080, "HTTP server port")
	return dagVizCmd
}

func runDagVizCmd(cmd *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	port, _ := cmd.Flags().GetInt("port")
	txStore := glb.TxBytesStore()
	rawDB := glb.TxBytesDBRaw()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dagVizHTML))
	})
	http.HandleFunc("/api/past_cone", func(w http.ResponseWriter, r *http.Request) {
		servePastCone(w, r, txStore)
	})
	http.HandleFunc("/api/slot", func(w http.ResponseWriter, r *http.Request) {
		serveSlot(w, r, txStore, rawDB)
	})
	http.HandleFunc("/api/find_tx", func(w http.ResponseWriter, r *http.Request) {
		serveFindTx(w, r, rawDB)
	})

	addr := fmt.Sprintf(":%d", port)
	glb.Infof("DAG explorer listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		glb.Assertf(false, "HTTP server failed: %v", err)
	}
}

// dagviz data types for JSON response

type dagVizVertex struct {
	ID          string `json:"id"`
	ShortID     string `json:"short_id"`
	Slot        uint32 `json:"slot"`
	Tick        byte   `json:"tick"`
	IsSequencer bool   `json:"is_seq"`
	IsBranch    bool   `json:"is_branch"`
	SeqChainID  string `json:"seq_chain_id,omitempty"`
	NumInputs   int    `json:"num_inputs"`
	NumOutputs  int    `json:"num_outputs"`
	IsTip       bool   `json:"is_tip,omitempty"`
	IsLeaf      bool   `json:"is_leaf,omitempty"`
	IsMissing   bool   `json:"is_missing,omitempty"`
}

type dagVizEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Type  string `json:"type"` // "input", "endorsement", "baseline"
	Label string `json:"label,omitempty"`
}

type dagVizData struct {
	Vertices []dagVizVertex `json:"vertices"`
	Edges    []dagVizEdge   `json:"edges"`
	TipID    string         `json:"tip_id,omitempty"`
}

// loader collects vertices and edges from the txstore

type dagVizLoader struct {
	txStore global.TxBytesGet
	txCache map[base.TransactionID]*transaction.Transaction
	visited map[base.TransactionID]bool
	data    dagVizData
}

func (l *dagVizLoader) load(txid base.TransactionID, depth int, isTip bool) {
	if l.visited[txid] {
		return
	}
	l.visited[txid] = true

	txBytesWithMeta := l.txStore.GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMeta) == 0 {
		l.data.Vertices = append(l.data.Vertices, dagVizVertex{
			ID:        hex.EncodeToString(txid.Bytes()),
			ShortID:   txid.StringShort(),
			Slot:      txid.Slot(),
			Tick:      txid.Tick(),
			IsMissing: true,
		})
		return
	}

	_, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMeta)
	if err != nil {
		return
	}
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return
	}
	l.txCache[txid] = tx

	vtx := dagVizVertex{
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
			vtx.SeqChainID = seqData.SequencerID.StringShort()
		}
	}
	l.data.Vertices = append(l.data.Vertices, vtx)

	if depth <= 0 {
		return
	}

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
		l.data.Edges = append(l.data.Edges, dagVizEdge{
			From:  hex.EncodeToString(txid.Bytes()),
			To:    hex.EncodeToString(inpTxID.Bytes()),
			Type:  "input",
			Label: label,
		})
	}

	// load endorsements
	for i := 0; i < tx.NumEndorsements(); i++ {
		endID := tx.MustEndorsementAt(byte(i))
		l.load(endID, depth-1, false)
		l.data.Edges = append(l.data.Edges, dagVizEdge{
			From: hex.EncodeToString(txid.Bytes()),
			To:   hex.EncodeToString(endID.Bytes()),
			Type: "endorsement",
		})
	}

	// explicit baseline
	if baselineID, ok := tx.ExplicitBaseline(); ok {
		l.load(baselineID, depth-1, false)
		l.data.Edges = append(l.data.Edges, dagVizEdge{
			From: hex.EncodeToString(txid.Bytes()),
			To:   hex.EncodeToString(baselineID.Bytes()),
			Type: "baseline",
		})
	}
}

func servePastCone(w http.ResponseWriter, r *http.Request, txStore global.TxBytesGet) {
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

	loader := &dagVizLoader{
		txStore: txStore,
		txCache: make(map[base.TransactionID]*transaction.Transaction),
		visited: make(map[base.TransactionID]bool),
	}
	loader.load(txid, depth, true)
	loader.data.TipID = hex.EncodeToString(txid.Bytes())

	sortVertices(loader.data.Vertices)
	ensureNonNil(&loader.data)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(loader.data)
}

// serveSlot returns all transactions in a given slot with their edges (inputs/endorsements loaded at depth 0)
func serveSlot(w http.ResponseWriter, r *http.Request, txStore global.TxBytesGet, rawDB *badger_adaptor.DB) {
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

	// collect all txids in this slot
	prefix := base.Slot2Bytes(slot)
	txids := make([]base.TransactionID, 0)
	rawDB.Iterator(prefix).IterateKeys(func(k []byte) bool {
		txid, err := base.TransactionIDFromBytes(k)
		if err == nil {
			txids = append(txids, txid)
		}
		return true
	})

	loader := &dagVizLoader{
		txStore: txStore,
		txCache: make(map[base.TransactionID]*transaction.Transaction),
		visited: make(map[base.TransactionID]bool),
	}
	// load each tx at depth 0 (just the vertex + edges, no recursion into dependencies)
	for _, txid := range txids {
		loader.load(txid, 0, false)
	}

	sortVertices(loader.data.Vertices)
	ensureNonNil(&loader.data)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(loader.data)
}

// serveFindTx searches for transactions matching a prefix.
// Accepts short format like "220942|36" or "220942|36sq]0066f7" or plain hex prefix.
func serveFindTx(w http.ResponseWriter, r *http.Request, rawDB *badger_adaptor.DB) {
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
		rawDB.Iterator(prefix).IterateKeys(func(k []byte) bool {
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
		rawDB.Iterator(prefixBytes).IterateKeys(func(k []byte) bool {
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
func ensureNonNil(d *dagVizData) {
	if d.Vertices == nil {
		d.Vertices = []dagVizVertex{}
	}
	if d.Edges == nil {
		d.Edges = []dagVizEdge{}
	}
}

func sortVertices(vertices []dagVizVertex) {
	sort.Slice(vertices, func(i, j int) bool {
		vi, vj := vertices[i], vertices[j]
		if vi.Slot != vj.Slot {
			return vi.Slot < vj.Slot
		}
		return vi.Tick < vj.Tick
	})
}

const dagVizHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Proxima DAG Explorer</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: "Consolas", "Monaco", monospace; background: #1a1a2e; color: #e0e0e0; display: flex; height: 100vh; overflow: hidden; }
#sidebar { width: 340px; min-width: 340px; background: #16213e; padding: 12px; display: flex; flex-direction: column; gap: 8px; border-right: 1px solid #333; overflow-y: auto; }
#sidebar h2 { font-size: 14px; color: #6dd5ed; margin-bottom: 2px; }
.section { margin-bottom: 6px; }
.section-title { font-size: 11px; color: #6dd5ed; margin-bottom: 4px; text-transform: uppercase; }
#sidebar label { font-size: 11px; color: #aaa; }
#sidebar input, #sidebar select { width: 100%; padding: 5px 7px; background: #0f3460; border: 1px solid #444; color: #e0e0e0; border-radius: 3px; font-family: inherit; font-size: 12px; }
#sidebar button { padding: 6px 10px; background: #0f3460; border: 1px solid #6dd5ed; color: #6dd5ed; cursor: pointer; border-radius: 3px; font-family: inherit; font-size: 11px; }
#sidebar button:hover { background: #1a4a8a; }
.btn-row { display: flex; gap: 6px; }
.btn-row button { flex: 1; }
#search-results { max-height: 150px; overflow-y: auto; font-size: 11px; }
#search-results div { padding: 3px 4px; cursor: pointer; border-bottom: 1px solid #222; }
#search-results div:hover { background: #0f3460; }
#details { font-size: 11px; line-height: 1.5; }
#details .field { color: #888; }
#details .val { color: #e0e0e0; word-break: break-all; }
#details .seq { color: #ffd700; }
#details .branch { color: #ff6b6b; }
#details .copyable { cursor: pointer; text-decoration: underline dotted; }
#details .copyable:hover { color: #6dd5ed; }
#legend { font-size: 10px; margin-top: 6px; }
#legend div { display: flex; align-items: center; gap: 6px; margin: 2px 0; }
#legend .swatch { width: 12px; height: 12px; border-radius: 2px; display: inline-block; }
#canvas-wrap { flex: 1; position: relative; overflow: hidden; }
svg { width: 100%; height: 100%; }
.node rect { stroke-width: 1.5; cursor: pointer; }
.node text { font-size: 10px; fill: #e0e0e0; pointer-events: none; }
.node.selected rect { stroke: #fff !important; stroke-width: 3; }
.edge { fill: none; stroke-width: 1.2; }
.edge.input { stroke: #888; }
.edge.endorsement { stroke: #ff6b6b; stroke-dasharray: 5,3; }
.edge.baseline { stroke: #6dd5ed; stroke-dasharray: 2,4; }
.edge-label { font-size: 8px; fill: #666; }
.tier-label { font-size: 10px; fill: #555; font-weight: bold; }
.tier-line { stroke: #252545; stroke-width: 0.5; }
#status { position: absolute; bottom: 8px; left: 8px; font-size: 10px; color: #666; background: rgba(26,26,46,0.8); padding: 2px 6px; border-radius: 3px; }
#nav-hint { position: absolute; top: 8px; right: 8px; font-size: 10px; color: #555; background: rgba(26,26,46,0.8); padding: 4px 8px; border-radius: 3px; }
</style>
</head>
<body>
<div id="sidebar">
  <h2>Proxima DAG Explorer</h2>

  <div class="section">
    <div class="section-title">Browse by slot</div>
    <div class="btn-row">
      <input id="slotInput" type="number" placeholder="slot number" style="flex:2">
      <button onclick="loadSlot()">Go</button>
    </div>
    <div class="btn-row" style="margin-top:4px">
      <button onclick="scrollSlot(-1)" title="older slot">&#x25BC; Slot -1</button>
      <button onclick="scrollSlot(+1)" title="newer slot">&#x25B2; Slot +1</button>
    </div>
  </div>

  <div class="section">
    <div class="section-title">Find transaction</div>
    <input id="findInput" placeholder="[slot|tick]hash.. or hex prefix" onkeydown="if(event.key==='Enter')findTx()">
    <div id="search-results"></div>
  </div>

  <div class="section">
    <div class="section-title">Past cone from tip</div>
    <div class="btn-row">
      <input id="txidInput" placeholder="full hex txid" style="flex:2">
      <input id="depthInput" type="number" value="6" min="1" max="30" style="width:50px;flex:0">
      <button onclick="loadPastCone()">Load</button>
    </div>
  </div>

  <hr style="border-color:#333">
  <div id="details"><div style="color:#666">Click a vertex to see details.<br>Double-click to explore its past cone.</div></div>
  <hr style="border-color:#333">
  <div id="legend">
    <div><span class="swatch" style="background:#4a6fa5"></span> Non-sequencer</div>
    <div><span class="swatch" style="background:#ffd700"></span> Sequencer</div>
    <div><span class="swatch" style="background:#ff6b6b"></span> Branch</div>
    <div><span class="swatch" style="background:#555"></span> Missing / Leaf</div>
    <div style="margin-top:4px"><span style="color:#888">&#x2500;&#x2500;</span> Input &nbsp; <span style="color:#ff6b6b">- -</span> Endorse &nbsp; <span style="color:#6dd5ed">&#xB7;&#xB7;</span> Baseline</div>
  </div>
</div>

<div id="canvas-wrap">
  <svg id="dag"></svg>
  <div id="status"></div>
  <div id="nav-hint">scroll: zoom &nbsp;|&nbsp; drag: pan &nbsp;|&nbsp; click: details &nbsp;|&nbsp; dbl-click: explore</div>
</div>

<script src="https://d3js.org/d3.v7.min.js"></script>
<script>
const NODE_W = 155, NODE_H = 26, TIER_GAP = 55, NODE_GAP = 15;
const seqColors = ["#ffd700","#ff8c00","#00cc88","#5dade2","#af7ac5","#f1948a","#45b39d","#f0b27a","#85c1e9"];
let currentData = null, currentSlot = null;

// --- Slot browsing ---

async function loadSlot() {
  const slot = parseInt(document.getElementById("slotInput").value);
  if (isNaN(slot)) return;
  currentSlot = slot;
  setStatus("Loading slot " + slot + "...");
  try {
    const resp = await fetch("/api/slot?slot=" + slot);
    if (!resp.ok) { alert(await resp.text()); return; }
    currentData = await resp.json();
    render(currentData);
    setStatus(currentData.vertices.length + " vertices in slot " + slot);
  } catch(e) { alert("Error: " + e); }
}

async function scrollSlot(delta) {
  if (currentSlot == null) {
    const s = parseInt(document.getElementById("slotInput").value);
    if (isNaN(s)) return;
    currentSlot = s;
  }
  currentSlot += delta;
  document.getElementById("slotInput").value = currentSlot;
  await loadSlot();
}

// --- Find tx by prefix ---

async function findTx() {
  const q = document.getElementById("findInput").value.trim();
  if (!q) return;
  const resultsDiv = document.getElementById("search-results");
  resultsDiv.innerHTML = '<div style="color:#666">Searching...</div>';
  try {
    const resp = await fetch("/api/find_tx?q=" + encodeURIComponent(q));
    if (!resp.ok) { resultsDiv.innerHTML = '<div style="color:#ff6b6b">' + (await resp.text()) + '</div>'; return; }
    const results = await resp.json();
    if (!results || results.length === 0) {
      resultsDiv.innerHTML = '<div style="color:#666">No matches found.</div>';
      return;
    }
    resultsDiv.innerHTML = results.map(r =>
      '<div onclick="selectFoundTx(\'' + r.id + '\')" title="' + r.id + '">' + r.short_id + '</div>'
    ).join("");
  } catch(e) { resultsDiv.innerHTML = '<div style="color:#ff6b6b">Error: ' + e + '</div>'; }
}

function selectFoundTx(id) {
  document.getElementById("txidInput").value = id;
  document.getElementById("search-results").innerHTML = '';
  loadPastCone();
}

// --- Past cone ---

async function loadPastCone() {
  const txid = document.getElementById("txidInput").value.trim();
  const depth = document.getElementById("depthInput").value;
  if (!txid) return;
  currentSlot = null;
  setStatus("Loading past cone...");
  try {
    const resp = await fetch("/api/past_cone?txid=" + encodeURIComponent(txid) + "&depth=" + depth);
    if (!resp.ok) { alert(await resp.text()); return; }
    currentData = await resp.json();
    render(currentData);
    setStatus(currentData.vertices.length + " vertices, " + currentData.edges.length + " edges");
  } catch(e) { alert("Error: " + e); }
}

// --- Rendering ---

function render(data) {
  const svg = d3.select("#dag");
  svg.selectAll("*").remove();
  if (!data.vertices || data.vertices.length === 0) { setStatus("No vertices."); return; }

  // chain color assignment
  const chainColorMap = {};
  let colorIdx = 0;
  data.vertices.forEach(v => {
    if (v.seq_chain_id && !chainColorMap[v.seq_chain_id]) {
      chainColorMap[v.seq_chain_id] = seqColors[colorIdx++ % seqColors.length];
    }
  });

  // tier grouping by (slot, tick)
  const tierKey = v => v.slot * 256 + v.tick;
  const tierMap = new Map();
  data.vertices.forEach(v => {
    const k = tierKey(v);
    if (!tierMap.has(k)) tierMap.set(k, []);
    tierMap.get(k).push(v);
  });
  const tierKeys = Array.from(tierMap.keys()).sort((a, b) => a - b);

  // layout: y positions (time ascending = bottom to top)
  const numTiers = tierKeys.length;
  const totalH = (numTiers + 1) * TIER_GAP;
  const posMap = {};

  tierKeys.forEach((tk, tierIdx) => {
    const nodes = tierMap.get(tk);
    const y = totalH - (tierIdx + 1) * TIER_GAP;
    const totalW = nodes.length * (NODE_W + NODE_GAP) - NODE_GAP;
    const startX = -totalW / 2;
    nodes.forEach((v, i) => {
      posMap[v.id] = { x: startX + i * (NODE_W + NODE_GAP) + NODE_W / 2, y };
    });
  });

  // barycenter passes for x improvement
  for (let pass = 0; pass < 10; pass++) {
    tierKeys.forEach(tk => {
      const nodes = tierMap.get(tk);
      nodes.forEach(v => {
        const cx = [];
        data.edges.forEach(e => {
          if (e.from === v.id && posMap[e.to]) cx.push(posMap[e.to].x);
          if (e.to === v.id && posMap[e.from]) cx.push(posMap[e.from].x);
        });
        if (cx.length > 0) {
          const avg = cx.reduce((a, b) => a + b, 0) / cx.length;
          posMap[v.id].x = posMap[v.id].x * 0.3 + avg * 0.7;
        }
      });
      // collision avoidance
      nodes.sort((a, b) => posMap[a.id].x - posMap[b.id].x);
      for (let i = 1; i < nodes.length; i++) {
        const gap = NODE_W + NODE_GAP;
        if (posMap[nodes[i].id].x - posMap[nodes[i-1].id].x < gap) {
          posMap[nodes[i].id].x = posMap[nodes[i-1].id].x + gap;
        }
      }
    });
  }

  // bounding box
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  data.vertices.forEach(v => {
    const p = posMap[v.id]; if (!p) return;
    minX = Math.min(minX, p.x - NODE_W/2); maxX = Math.max(maxX, p.x + NODE_W/2);
    minY = Math.min(minY, p.y - NODE_H/2); maxY = Math.max(maxY, p.y + NODE_H/2);
  });
  const pad = 50;
  minX -= pad; minY -= pad; maxX += pad; maxY += pad;

  const g = svg.append("g");

  // zoom + pan
  const zoom = d3.zoom().scaleExtent([0.05, 8]).on("zoom", e => g.attr("transform", e.transform));
  svg.call(zoom);

  // fit to view
  const W = svg.node().clientWidth, H = svg.node().clientHeight;
  const bw = maxX - minX, bh = maxY - minY;
  const scale = Math.min(W / bw, H / bh, 2) * 0.85;
  const tx = W/2 - (minX + bw/2) * scale, ty = H/2 - (minY + bh/2) * scale;
  svg.call(zoom.transform, d3.zoomIdentity.translate(tx, ty).scale(scale));

  // arrow markers
  const defs = svg.append("defs");
  [["input","#888"],["endorsement","#ff6b6b"],["baseline","#6dd5ed"]].forEach(([t, c]) => {
    defs.append("marker").attr("id","arr-"+t).attr("viewBox","0 0 10 10")
      .attr("refX",10).attr("refY",5).attr("markerWidth",6).attr("markerHeight",6).attr("orient","auto")
      .append("path").attr("d","M0,0 L10,5 L0,10 Z").attr("fill",c);
  });

  // tier lines + labels
  tierKeys.forEach(tk => {
    const nodes = tierMap.get(tk);
    const y = posMap[nodes[0].id].y;
    const slot = Math.floor(tk / 256), tick = tk % 256;
    g.append("line").attr("class","tier-line")
      .attr("x1", minX).attr("x2", maxX).attr("y1", y).attr("y2", y);
    g.append("text").attr("class","tier-label")
      .attr("x", minX + 4).attr("y", y - 5).text("[" + slot + "|" + tick + "]");
  });

  // edges
  const edgeG = g.append("g");
  data.edges.forEach(e => {
    const from = posMap[e.from], to = posMap[e.to];
    if (!from || !to) return;
    const dx = to.x - from.x;
    const cx = (from.x + to.x)/2 + dx * 0.08;
    const cy = (from.y + to.y)/2;
    edgeG.append("path").attr("class", "edge " + e.type)
      .attr("d", "M"+from.x+","+(from.y+NODE_H/2)+" Q"+cx+","+cy+" "+to.x+","+(to.y-NODE_H/2))
      .attr("marker-end", "url(#arr-"+e.type+")");
    if (e.label) {
      edgeG.append("text").attr("class","edge-label")
        .attr("x", cx).attr("y", cy-3).attr("text-anchor","middle").text(e.label);
    }
  });

  // nodes
  const nodeG = g.selectAll(".node").data(data.vertices).enter().append("g")
    .attr("class","node")
    .attr("transform", d => { const p=posMap[d.id]; return "translate("+(p.x-NODE_W/2)+","+(p.y-NODE_H/2)+")"; })
    .on("click", (ev, d) => selectNode(d, data))
    .on("dblclick", (ev, d) => { document.getElementById("txidInput").value = d.id; loadPastCone(); });

  nodeG.append("rect").attr("width",NODE_W).attr("height",NODE_H).attr("rx",4).attr("ry",4)
    .attr("fill", d => {
      if (d.is_missing) return "#333";
      if (d.is_branch) return "#5a2020";
      if (d.is_seq && d.seq_chain_id) return dim(chainColorMap[d.seq_chain_id]||"#ffd700", 0.35);
      return "#2a3a5a";
    })
    .attr("stroke", d => {
      if (d.is_tip) return "#fff";
      if (d.is_missing || d.is_leaf) return "#555";
      if (d.is_branch) return "#ff6b6b";
      if (d.is_seq && d.seq_chain_id) return chainColorMap[d.seq_chain_id]||"#ffd700";
      return "#4a6fa5";
    });

  nodeG.append("text").attr("x",NODE_W/2).attr("y",NODE_H/2+3).attr("text-anchor","middle")
    .text(d => d.short_id);
}

function selectNode(d, data) {
  d3.selectAll(".node").classed("selected", false);
  d3.selectAll(".node").filter(n => n.id === d.id).classed("selected", true);

  const inE = data.edges.filter(e => e.from===d.id && e.type==="input");
  const enE = data.edges.filter(e => e.from===d.id && e.type==="endorsement");
  const blE = data.edges.filter(e => e.from===d.id && e.type==="baseline");

  let h = '<div>';
  h += '<div><span class="field">ID:</span> <span class="copyable val" onclick="copyTxt(this)" title="click to copy">' + d.id + '</span></div>';
  h += '<div><span class="field">Short:</span> <span class="val">' + d.short_id + '</span></div>';
  h += '<div><span class="field">Slot:</span> <span class="val">' + d.slot + '</span> <span class="field">Tick:</span> <span class="val">' + d.tick + '</span></div>';
  if (d.is_branch) h += '<div class="branch">BRANCH</div>';
  else if (d.is_seq) h += '<div class="seq">SEQUENCER</div>';
  if (d.seq_chain_id) h += '<div><span class="field">Chain:</span> <span class="seq">' + d.seq_chain_id + '</span></div>';
  if (d.is_missing) h += '<div style="color:#ff6b6b">NOT IN TXSTORE</div>';
  if (d.is_leaf && !d.is_missing) h += '<div style="color:#888">DEPTH LIMIT</div>';
  h += '<div><span class="field">In:</span> ' + d.num_inputs + ' <span class="field">Out:</span> ' + d.num_outputs + '</div>';

  const edgeList = (edges, label, color) => {
    if (!edges.length) return '';
    let s = '<div style="margin-top:4px"><span class="field">' + label + ':</span></div>';
    edges.forEach(e => {
      const sh = findShort(data, e.to);
      s += '<div style="padding-left:8px;color:'+color+'"><span class="copyable" onclick="selFoundTx(\''+e.to+'\')">' + sh + '</span> ' + (e.label||'') + '</div>';
    });
    return s;
  };
  h += edgeList(inE, "Inputs", "#ccc");
  h += edgeList(enE, "Endorsements", "#ff6b6b");
  h += edgeList(blE, "Baseline", "#6dd5ed");
  h += '</div>';
  document.getElementById("details").innerHTML = h;
}

function selFoundTx(id) { document.getElementById("txidInput").value = id; loadPastCone(); }
function findShort(data, id) { const v = data.vertices.find(v => v.id===id); return v ? v.short_id : id.substring(0,16)+".."; }
function copyTxt(el) { navigator.clipboard.writeText(el.textContent); el.style.color="#6dd5ed"; setTimeout(()=>el.style.color="",500); }
function setStatus(s) { document.getElementById("status").textContent = s; }
function dim(hex, factor) {
  hex = hex.replace("#","");
  const r = Math.round(parseInt(hex.substring(0,2),16)*factor);
  const g = Math.round(parseInt(hex.substring(2,4),16)*factor);
  const b = Math.round(parseInt(hex.substring(4,6),16)*factor);
  return "#"+[r,g,b].map(c=>Math.max(0,Math.min(255,c)).toString(16).padStart(2,"0")).join("");
}

document.getElementById("slotInput").addEventListener("keydown", e => { if(e.key==="Enter") loadSlot(); });
document.getElementById("findInput").addEventListener("keydown", e => { if(e.key==="Enter") findTx(); });
document.getElementById("txidInput").addEventListener("keydown", e => { if(e.key==="Enter") loadPastCone(); });
</script>
</body>
</html>
`
