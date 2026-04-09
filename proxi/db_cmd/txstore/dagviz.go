package txstore

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
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

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dagVizHTML))
	})
	http.HandleFunc("/api/past_cone", func(w http.ResponseWriter, r *http.Request) {
		servePastCone(w, r, txStore)
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
	TipID    string         `json:"tip_id"`
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

	// sort vertices by time for deterministic output
	sort.Slice(loader.data.Vertices, func(i, j int) bool {
		vi, vj := loader.data.Vertices[i], loader.data.Vertices[j]
		if vi.Slot != vj.Slot {
			return vi.Slot < vj.Slot
		}
		return vi.Tick < vj.Tick
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(loader.data)
}

const dagVizHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Proxima DAG Explorer</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: "Consolas", "Monaco", monospace; background: #1a1a2e; color: #e0e0e0; display: flex; height: 100vh; overflow: hidden; }
#sidebar { width: 320px; min-width: 320px; background: #16213e; padding: 12px; display: flex; flex-direction: column; gap: 10px; border-right: 1px solid #333; overflow-y: auto; }
#sidebar h2 { font-size: 14px; color: #6dd5ed; margin-bottom: 4px; }
#sidebar label { font-size: 11px; color: #aaa; }
#sidebar input, #sidebar select { width: 100%; padding: 6px 8px; background: #0f3460; border: 1px solid #444; color: #e0e0e0; border-radius: 3px; font-family: inherit; font-size: 12px; }
#sidebar button { padding: 8px; background: #0f3460; border: 1px solid #6dd5ed; color: #6dd5ed; cursor: pointer; border-radius: 3px; font-family: inherit; font-size: 12px; }
#sidebar button:hover { background: #1a4a8a; }
#details { font-size: 11px; line-height: 1.5; }
#details .field { color: #888; }
#details .val { color: #e0e0e0; }
#details .seq { color: #ffd700; }
#details .branch { color: #ff6b6b; }
#details .copyable { cursor: pointer; text-decoration: underline dotted; }
#details .copyable:hover { color: #6dd5ed; }
#legend { font-size: 10px; margin-top: 8px; }
#legend div { display: flex; align-items: center; gap: 6px; margin: 2px 0; }
#legend .swatch { width: 14px; height: 14px; border-radius: 2px; display: inline-block; }
#canvas-wrap { flex: 1; position: relative; overflow: hidden; }
svg { width: 100%; height: 100%; }
.node rect { stroke-width: 1.5; cursor: pointer; }
.node text { font-size: 10px; fill: #e0e0e0; pointer-events: none; }
.node.selected rect { stroke: #fff; stroke-width: 3; }
.edge { fill: none; stroke-width: 1.2; }
.edge.input { stroke: #888; }
.edge.endorsement { stroke: #ff6b6b; stroke-dasharray: 5,3; }
.edge.baseline { stroke: #6dd5ed; stroke-dasharray: 2,4; }
.edge-label { font-size: 8px; fill: #666; }
.tier-label { font-size: 10px; fill: #444; }
.tier-line { stroke: #222; stroke-width: 0.5; stroke-dasharray: 4,4; }
#status { position: absolute; bottom: 8px; left: 8px; font-size: 10px; color: #666; }
#zoom-info { position: absolute; bottom: 8px; right: 8px; font-size: 10px; color: #666; }
</style>
</head>
<body>
<div id="sidebar">
  <h2>Proxima DAG Explorer</h2>
  <div>
    <label>Transaction ID (hex)</label>
    <input id="txid" placeholder="full hex transaction ID">
  </div>
  <div>
    <label>Depth</label>
    <input id="depth" type="number" value="6" min="1" max="30">
  </div>
  <button id="loadBtn" onclick="loadDAG()">Load Past Cone</button>
  <hr style="border-color:#333">
  <div id="details">
    <div style="color:#666">Click a vertex to see details.</div>
  </div>
  <hr style="border-color:#333">
  <div id="legend">
    <div><span class="swatch" style="background:#4a6fa5"></span> Non-sequencer</div>
    <div><span class="swatch" style="background:#ffd700"></span> Sequencer</div>
    <div><span class="swatch" style="background:#ff6b6b"></span> Branch</div>
    <div><span class="swatch" style="background:#555"></span> Missing / Leaf</div>
    <div style="margin-top:4px"><span style="color:#888">&#x2500;&#x2500;&#x2500;</span> Input</div>
    <div><span style="color:#ff6b6b">- - -</span> Endorsement</div>
    <div><span style="color:#6dd5ed">&#xB7; &#xB7; &#xB7;</span> Baseline</div>
  </div>
</div>
<div id="canvas-wrap">
  <svg id="dag"></svg>
  <div id="status"></div>
  <div id="zoom-info"></div>
</div>

<script>
// d3 v7 minimal inline (only what we need: selections, zoom, drag)
// We load from CDN for simplicity
</script>
<script src="https://d3js.org/d3.v7.min.js"></script>
<script>
const NODE_W = 160, NODE_H = 28, TIER_GAP = 60, NODE_GAP = 20;
const seqColors = [
  "#ffd700","#ff8c00","#00cc88","#5dade2","#af7ac5","#f1948a","#45b39d","#f0b27a","#85c1e9"
];
let currentData = null, selectedNode = null;

async function loadDAG() {
  const txid = document.getElementById("txid").value.trim();
  const depth = document.getElementById("depth").value;
  if (!txid) return;
  document.getElementById("loadBtn").textContent = "Loading...";
  try {
    const resp = await fetch("/api/past_cone?txid=" + encodeURIComponent(txid) + "&depth=" + depth);
    if (!resp.ok) { alert(await resp.text()); return; }
    currentData = await resp.json();
    render(currentData);
  } catch(e) { alert("Error: " + e); }
  finally { document.getElementById("loadBtn").textContent = "Load Past Cone"; }
}

function render(data) {
  const svg = d3.select("#dag");
  svg.selectAll("*").remove();

  if (!data.vertices || data.vertices.length === 0) {
    document.getElementById("status").textContent = "No vertices found.";
    return;
  }

  // assign sequencer chain colors
  const chainColorMap = {};
  let colorIdx = 0;
  data.vertices.forEach(v => {
    if (v.seq_chain_id && !chainColorMap[v.seq_chain_id]) {
      chainColorMap[v.seq_chain_id] = seqColors[colorIdx % seqColors.length];
      colorIdx++;
    }
  });

  // build tier map: group by (slot, tick)
  const tierKey = v => v.slot * 256 + v.tick;
  const tierMap = new Map();
  data.vertices.forEach(v => {
    const k = tierKey(v);
    if (!tierMap.has(k)) tierMap.set(k, []);
    tierMap.get(k).push(v);
  });

  // sort tiers ascending by time
  const tierKeys = Array.from(tierMap.keys()).sort((a, b) => a - b);

  // layout: y from bottom (oldest) to top (newest)
  const numTiers = tierKeys.length;
  const totalH = numTiers * TIER_GAP + 100;
  const posMap = {}; // id -> {x, y}

  tierKeys.forEach((tk, tierIdx) => {
    const nodes = tierMap.get(tk);
    const y = totalH - (tierIdx + 1) * TIER_GAP;
    const totalW = nodes.length * (NODE_W + NODE_GAP) - NODE_GAP;
    const startX = -totalW / 2;
    nodes.forEach((v, i) => {
      posMap[v.id] = { x: startX + i * (NODE_W + NODE_GAP) + NODE_W / 2, y: y };
    });
  });

  // improve x positions: pull towards average of connected nodes (barycenter passes)
  const idSet = new Set(data.vertices.map(v => v.id));
  for (let pass = 0; pass < 8; pass++) {
    tierKeys.forEach(tk => {
      const nodes = tierMap.get(tk);
      nodes.forEach(v => {
        // find connected nodes (targets of edges from this vertex)
        const connected = [];
        data.edges.forEach(e => {
          if (e.from === v.id && posMap[e.to]) connected.push(posMap[e.to].x);
          if (e.to === v.id && posMap[e.from]) connected.push(posMap[e.from].x);
        });
        if (connected.length > 0) {
          const avg = connected.reduce((a, b) => a + b, 0) / connected.length;
          posMap[v.id].x = posMap[v.id].x * 0.3 + avg * 0.7;
        }
      });
      // prevent overlap within tier
      nodes.sort((a, b) => posMap[a.id].x - posMap[b.id].x);
      for (let i = 1; i < nodes.length; i++) {
        const prev = posMap[nodes[i-1].id].x;
        const curr = posMap[nodes[i].id].x;
        if (curr - prev < NODE_W + NODE_GAP) {
          posMap[nodes[i].id].x = prev + NODE_W + NODE_GAP;
        }
      }
    });
  }

  // compute bounding box
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  data.vertices.forEach(v => {
    const p = posMap[v.id];
    if (!p) return;
    minX = Math.min(minX, p.x - NODE_W/2);
    maxX = Math.max(maxX, p.x + NODE_W/2);
    minY = Math.min(minY, p.y - NODE_H/2);
    maxY = Math.max(maxY, p.y + NODE_H/2);
  });
  const pad = 60;
  minX -= pad; minY -= pad; maxX += pad; maxY += pad;

  const g = svg.append("g");

  // zoom
  const zoom = d3.zoom()
    .scaleExtent([0.1, 5])
    .on("zoom", e => {
      g.attr("transform", e.transform);
      document.getElementById("zoom-info").textContent = "zoom: " + e.transform.k.toFixed(2);
    });
  svg.call(zoom);

  // fit to view on load
  const svgEl = svg.node();
  const W = svgEl.clientWidth, H = svgEl.clientHeight;
  const bw = maxX - minX, bh = maxY - minY;
  const scale = Math.min(W / bw, H / bh, 1.5) * 0.9;
  const tx = W / 2 - (minX + bw / 2) * scale;
  const ty = H / 2 - (minY + bh / 2) * scale;
  svg.call(zoom.transform, d3.zoomIdentity.translate(tx, ty).scale(scale));

  // tier lines and labels
  tierKeys.forEach(tk => {
    const nodes = tierMap.get(tk);
    const y = posMap[nodes[0].id].y;
    const slot = Math.floor(tk / 256);
    const tick = tk % 256;
    g.append("line")
      .attr("class", "tier-line")
      .attr("x1", minX).attr("x2", maxX)
      .attr("y1", y).attr("y2", y);
    g.append("text")
      .attr("class", "tier-label")
      .attr("x", minX + 4).attr("y", y - 4)
      .text("[" + slot + "|" + tick + "]");
  });

  // edges
  const edgeG = g.append("g");
  data.edges.forEach(e => {
    const from = posMap[e.from], to = posMap[e.to];
    if (!from || !to) return;

    // curved path to avoid overlaps
    const dx = to.x - from.x, dy = to.y - from.y;
    const cx = (from.x + to.x) / 2 + (dy === 0 ? 0 : dx * 0.1);
    const cy = (from.y + to.y) / 2;

    const path = edgeG.append("path")
      .attr("class", "edge " + e.type)
      .attr("d", "M" + from.x + "," + (from.y + NODE_H/2) +
                 " Q" + cx + "," + cy +
                 " " + to.x + "," + (to.y - NODE_H/2))
      .attr("marker-end", "url(#arrow-" + e.type + ")");

    if (e.label) {
      edgeG.append("text")
        .attr("class", "edge-label")
        .attr("x", cx).attr("y", cy - 3)
        .attr("text-anchor", "middle")
        .text(e.label);
    }
  });

  // arrow markers
  const defs = svg.append("defs");
  ["input", "endorsement", "baseline"].forEach(t => {
    const color = t === "endorsement" ? "#ff6b6b" : t === "baseline" ? "#6dd5ed" : "#888";
    defs.append("marker")
      .attr("id", "arrow-" + t)
      .attr("viewBox", "0 0 10 10")
      .attr("refX", 10).attr("refY", 5)
      .attr("markerWidth", 6).attr("markerHeight", 6)
      .attr("orient", "auto")
      .append("path")
      .attr("d", "M0,0 L10,5 L0,10 Z")
      .attr("fill", color);
  });

  // nodes
  const nodeG = g.selectAll(".node")
    .data(data.vertices)
    .enter().append("g")
    .attr("class", "node")
    .attr("transform", d => {
      const p = posMap[d.id];
      return "translate(" + (p.x - NODE_W/2) + "," + (p.y - NODE_H/2) + ")";
    })
    .on("click", (ev, d) => selectNode(d, data))
    .on("dblclick", (ev, d) => {
      document.getElementById("txid").value = d.id;
      loadDAG();
    });

  nodeG.append("rect")
    .attr("width", NODE_W).attr("height", NODE_H)
    .attr("rx", 4).attr("ry", 4)
    .attr("fill", d => {
      if (d.is_missing) return "#333";
      if (d.is_branch) return "#5a2020";
      if (d.is_seq && d.seq_chain_id) return adjustBrightness(chainColorMap[d.seq_chain_id] || "#ffd700", -40);
      return "#2a3a5a";
    })
    .attr("stroke", d => {
      if (d.is_tip) return "#fff";
      if (d.is_missing) return "#555";
      if (d.is_branch) return "#ff6b6b";
      if (d.is_seq && d.seq_chain_id) return chainColorMap[d.seq_chain_id] || "#ffd700";
      return "#4a6fa5";
    });

  nodeG.append("text")
    .attr("x", NODE_W / 2).attr("y", NODE_H / 2 + 3)
    .attr("text-anchor", "middle")
    .text(d => d.short_id);

  document.getElementById("status").textContent = data.vertices.length + " vertices, " + data.edges.length + " edges";
}

function selectNode(d, data) {
  d3.selectAll(".node").classed("selected", false);
  d3.selectAll(".node").filter(n => n.id === d.id).classed("selected", true);
  selectedNode = d;

  const det = document.getElementById("details");
  const inEdges = data.edges.filter(e => e.from === d.id && e.type === "input");
  const endEdges = data.edges.filter(e => e.from === d.id && e.type === "endorsement");
  const blEdges = data.edges.filter(e => e.from === d.id && e.type === "baseline");

  let html = '<div>';
  html += '<div><span class="field">ID:</span> <span class="copyable val" onclick="copyText(this)" title="click to copy">' + d.id + '</span></div>';
  html += '<div><span class="field">Short:</span> <span class="val">' + d.short_id + '</span></div>';
  html += '<div><span class="field">Slot:</span> <span class="val">' + d.slot + '</span> <span class="field">Tick:</span> <span class="val">' + d.tick + '</span></div>';

  if (d.is_branch) html += '<div class="branch">BRANCH TRANSACTION</div>';
  else if (d.is_seq) html += '<div class="seq">SEQUENCER TX</div>';
  if (d.seq_chain_id) html += '<div><span class="field">Chain:</span> <span class="seq">' + d.seq_chain_id + '</span></div>';
  if (d.is_missing) html += '<div style="color:#ff6b6b">NOT IN TXSTORE</div>';
  if (d.is_leaf) html += '<div style="color:#888">DEPTH LIMIT (leaf)</div>';

  html += '<div><span class="field">Inputs:</span> <span class="val">' + d.num_inputs + '</span> <span class="field">Outputs:</span> <span class="val">' + d.num_outputs + '</span></div>';

  if (inEdges.length > 0) {
    html += '<div style="margin-top:6px"><span class="field">Input edges:</span></div>';
    inEdges.forEach(e => {
      const short = findShort(data, e.to);
      html += '<div style="padding-left:8px"><span class="copyable" onclick="navTo(\'' + e.to + '\')" title="double-click node to explore">' + short + '</span> ' + (e.label || '') + '</div>';
    });
  }
  if (endEdges.length > 0) {
    html += '<div style="margin-top:4px"><span class="field">Endorsements:</span></div>';
    endEdges.forEach(e => {
      const short = findShort(data, e.to);
      html += '<div style="padding-left:8px;color:#ff6b6b"><span class="copyable" onclick="navTo(\'' + e.to + '\')">' + short + '</span></div>';
    });
  }
  if (blEdges.length > 0) {
    html += '<div style="margin-top:4px"><span class="field">Explicit baseline:</span></div>';
    blEdges.forEach(e => {
      const short = findShort(data, e.to);
      html += '<div style="padding-left:8px;color:#6dd5ed"><span class="copyable" onclick="navTo(\'' + e.to + '\')">' + short + '</span></div>';
    });
  }

  html += '</div>';
  det.innerHTML = html;
}

function findShort(data, id) {
  const v = data.vertices.find(v => v.id === id);
  return v ? v.short_id : id.substring(0, 16) + "..";
}

function navTo(id) {
  document.getElementById("txid").value = id;
  loadDAG();
}

function copyText(el) {
  navigator.clipboard.writeText(el.textContent);
  el.style.color = "#6dd5ed";
  setTimeout(() => { el.style.color = ""; }, 500);
}

function adjustBrightness(hex, amount) {
  hex = hex.replace("#", "");
  let r = Math.max(0, Math.min(255, parseInt(hex.substring(0,2), 16) + amount));
  let g = Math.max(0, Math.min(255, parseInt(hex.substring(2,4), 16) + amount));
  let b = Math.max(0, Math.min(255, parseInt(hex.substring(4,6), 16) + amount));
  return "#" + [r,g,b].map(c => c.toString(16).padStart(2,"0")).join("");
}

// keyboard shortcut: Enter in txid field triggers load
document.getElementById("txid").addEventListener("keydown", e => { if (e.key === "Enter") loadDAG(); });
</script>
</body>
</html>
`
