package memdag

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

var (
	fontsizeAttribute    = graph.VertexAttribute("fontsize", "10")
	simpleNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "blues3"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "2"),
		graph.VertexAttribute("fillcolor", "1"),
	}
	seqNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "paired9"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "9"),
	}
	finalTxAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "bugn9"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "9"),
		graph.VertexAttribute("fillcolor", "1"),
	}
	orphanedTxAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "bugn9"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "9"),
		graph.VertexAttribute("fillcolor", "1"),
	}
)

func sequencerNodeAttributes(v *vertex.Vertex, coverage uint64, dict map[ledger.ChainID]int) []func(*graph.VertexProperties) {
	seqID := v.Tx.SequencerTransactionData().SequencerID
	if _, found := dict[seqID]; !found {
		dict[seqID] = (len(dict) % 9) + 1
	}
	ret := make([]func(*graph.VertexProperties), len(seqNodeAttributes))
	copy(ret, seqNodeAttributes)
	ret = append(ret, graph.VertexAttribute("fillcolor", strconv.Itoa(dict[seqID])))
	if coverage > 0 {
		ret = append(ret, graph.VertexAttribute("xlabel", util.Th(coverage)))
	}
	return ret
}

func makeGraphNode(vid *vertex.WrappedTx, gr graph.Graph[string, string], seqDict map[ledger.ChainID]int, highlighted bool) {
	id := vid.IDVeryShort()
	attr := simpleNodeAttributes
	var err error

	status := vid.GetTxStatus()
	lcp := vid.GetLedgerCoverageP()
	lc := uint64(0)
	if lcp != nil {
		lc = *lcp
	}
	vid.RUnwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			if v.Tx.IsSequencerMilestone() {
				attr = sequencerNodeAttributes(v, lc, seqDict)
			}
			switch status {
			case vertex.Bad:
				attr = append(attr, graph.VertexAttribute("shape", "invtriangle"))
			case vertex.Undefined:
				attr = append(attr, graph.VertexAttribute("shape", "diamond"))
			case vertex.Good:
				if v.Tx.IsBranchTransaction() {
					attr = append(attr, graph.VertexAttribute("shape", "box"))
				}
			}
			if highlighted {
				attr = append(attr, graph.VertexAttribute("penwidth", "3"))
			}
			err = gr.AddVertex(id, attr...)
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			err = gr.AddVertex(id, finalTxAttributes...)
		},
		Deleted: func() {
			err = gr.AddVertex(id, orphanedTxAttributes...)
		},
	})
	util.AssertNoError(err)
	if vid.GetTxStatus() == vertex.Bad {
		attr = append(attr, graph.VertexAttribute("color", "ret"))
	}
}

var nilCount int

func makeGraphEdges(vid *vertex.WrappedTx, gr graph.Graph[string, string]) {
	id := vid.IDVeryShort()
	vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		v.ForEachInputDependency(func(i byte, inp *vertex.WrappedTx) bool {
			if inp == nil {
				idNil := fmt.Sprintf("%d", nilCount)
				oid := v.Tx.MustInputAt(i)
				err := gr.AddVertex(idNil,
					graph.VertexAttribute("shape", "point"),
					graph.VertexAttribute("xlabel", oid.StringVeryShort()),
					graph.VertexAttribute("fontsize", "10"),
				)
				util.AssertNoError(err)
				nilCount++
				err = gr.AddEdge(id, idNil)
				util.AssertNoError(err)
				return true
			}
			o := v.GetConsumedOutput(i)
			outIndex := v.Tx.MustOutputIndexOfTheInput(i)
			amountStr := "???"
			if o != nil {
				amountStr = util.Th(o.Amount())
			}
			edgeAttributes := []func(_ *graph.EdgeProperties){
				graph.EdgeAttribute("label", fmt.Sprintf("%s(#%d)", amountStr, outIndex)),
				graph.EdgeAttribute("fontsize", "10"),
			}
			_ = gr.AddEdge(id, inp.IDVeryShort(), edgeAttributes...)
			return true
		})
		v.ForEachEndorsement(func(i byte, vEnd *vertex.WrappedTx) bool {
			if vEnd == nil {
				idNil := fmt.Sprintf("%d", nilCount)
				err := gr.AddVertex(idNil, graph.VertexAttribute("shape", "point"))
				util.AssertNoError(err)
				nilCount++
				err = gr.AddEdge(id, idNil)
				util.AssertNoError(err)
				return true
			}
			_ = gr.AddEdge(id, vEnd.IDVeryShort(), graph.EdgeAttribute("color", "red"))
			//util.Assertf(err == nil || errors.Is(err, graph.ErrEdgeAlreadyExists), "%v", err)
			return true
		})
	}})
}

func (d *MemDAG) MakeGraph(additionalVertices ...*vertex.WrappedTx) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	vertices := d.Vertices()
	seqDict := make(map[ledger.ChainID]int)
	for _, vid := range vertices {
		makeGraphNode(vid, ret, seqDict, false)
	}
	for _, vid := range additionalVertices {
		makeGraphNode(vid, ret, seqDict, true)
	}
	for _, vid := range vertices {
		makeGraphEdges(vid, ret)
	}
	for _, vid := range additionalVertices {
		makeGraphEdges(vid, ret)
	}
	return ret
}

func (d *MemDAG) SaveGraph(fname string) {
	gr := d.MakeGraph()
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func MakeGraphPastCone(vid *vertex.WrappedTx, maxVertices ...int) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	maxx := math.MaxUint16
	if len(maxVertices) > 0 && maxVertices[0] < math.MaxUint16 {
		maxx = maxVertices[0]
	}

	seqDict := make(map[ledger.ChainID]int)
	count := 0

	mkNode := func(vidCur *vertex.WrappedTx) bool {
		if count > maxx {
			return false
		}
		count++
		makeGraphNode(vidCur, ret, seqDict, false)
		return true
	}
	vid.TraversePastConeDepthFirst(vertex.UnwrapOptionsForTraverse{
		Vertex: func(vidCur *vertex.WrappedTx, _ *vertex.Vertex) bool {
			return mkNode(vidCur)
		},
		VirtualTx: func(vidCur *vertex.WrappedTx, vCur *vertex.VirtualTransaction) bool {
			return mkNode(vidCur)
		},
		Deleted: func(vidCur *vertex.WrappedTx) bool {
			return mkNode(vidCur)
		},
	})
	count = 0
	vid.TraversePastConeDepthFirst(vertex.UnwrapOptionsForTraverse{
		Vertex: func(vidCur *vertex.WrappedTx, _ *vertex.Vertex) bool {
			makeGraphEdges(vidCur, ret)
			return true
		},
	})
	return ret
}

func SaveGraphPastCone(vid *vertex.WrappedTx, fname string) {
	gr := MakeGraphPastCone(vid, 500)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func (d *MemDAG) SaveTree(fname string) {
	multistate.SaveBranchTree(d.StateStore(), fname)
}

func (d *MemDAG) SaveSequencerGraph(fname string) {
	gr := d.MakeSequencerGraph()
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func (d *MemDAG) MakeSequencerGraph() graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	seqDict := make(map[ledger.ChainID]int)
	seqVertices := make([]*vertex.WrappedTx, 0)
	for _, vid := range d.Vertices() {
		if !vid.IsSequencerMilestone() {
			continue
		}
		makeGraphNode(vid, ret, seqDict, false)
		seqVertices = append(seqVertices, vid)
	}
	for _, vid := range seqVertices {
		makeSequencerGraphEdges(vid, ret)
	}
	return ret
}

func makeSequencerGraphEdges(vid *vertex.WrappedTx, gr graph.Graph[string, string]) {
	id := vid.IDVeryShort()

	vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		var stemInputIdx, seqInputIdx byte
		if vid.IsBranchTransaction() {
			stemInputIdx = v.StemInputIndex()
		}
		seqInputIdx = v.SequencerInputIndex()

		v.ForEachInputDependency(func(i byte, inp *vertex.WrappedTx) bool {
			if inp == nil {
				return true
			}
			if i == seqInputIdx || (vid.IsBranchTransaction() && i == stemInputIdx) {
				o := v.GetConsumedOutput(i)
				outIndex := v.Tx.MustOutputIndexOfTheInput(i)
				amountStr := "???"
				if o != nil {
					amountStr = util.Th(o.Amount())
				}
				edgeAttributes := []func(_ *graph.EdgeProperties){
					graph.EdgeAttribute("label", fmt.Sprintf("%s(#%d)", amountStr, outIndex)),
					graph.EdgeAttribute("fontsize", "10"),
				}
				_ = gr.AddEdge(id, inp.IDVeryShort(), edgeAttributes...)
			}
			return true
		})
		v.ForEachEndorsement(func(i byte, vEnd *vertex.WrappedTx) bool {
			if vEnd == nil {
				idNil := fmt.Sprintf("%d", nilCount)
				err := gr.AddVertex(idNil, graph.VertexAttribute("shape", "point"))
				util.AssertNoError(err)
				nilCount++
				err = gr.AddEdge(id, idNil)
				util.AssertNoError(err)
				return true
			}
			_ = gr.AddEdge(id, vEnd.IDVeryShort(), graph.EdgeAttribute("color", "red"))
			//util.Assertf(err == nil || errors.Is(err, graph.ErrEdgeAlreadyExists), "%v", err)
			return true
		})
	}})
}

// MakeDAGFromTxStore creates dummy MemDAG from past cones of tips. Only uses txBytes from txStore
// It is used in testing, to visualize real transaction MemDAG, not the pruned cache kept in the node
func MakeDAGFromTxStore(txStore global.TxBytesGet, oldestSlot ledger.Slot, tips ...ledger.TransactionID) *MemDAG {
	d := New(nil)
	for i := range tips {
		d.loadPastConeFromTxStore(tips[i], txStore, oldestSlot)
	}
	return d
}

// loadPastConeFromTxStore for generating graph only. Not thread safe
func (d *MemDAG) loadPastConeFromTxStore(txid ledger.TransactionID, txStore global.TxBytesGet, oldestSlot ledger.Slot) *vertex.WrappedTx {
	if txid.Slot() < oldestSlot {
		return nil
	}
	if vid, already := d.vertices[txid]; already {
		return vid
	}
	txBytesWithMetadata := txStore.GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMetadata) == 0 {
		return nil
	}
	_, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	util.AssertNoError(err)
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	util.AssertNoError(err)

	v := vertex.New(tx)
	for i := range v.Inputs {
		oid := tx.MustInputAt(byte(i))
		v.Inputs[i] = d.loadPastConeFromTxStore(oid.TransactionID(), txStore, oldestSlot)
	}
	for i := range v.Endorsements {
		endID := tx.EndorsementAt(byte(i))
		v.Endorsements[i] = d.loadPastConeFromTxStore(endID, txStore, oldestSlot)
	}
	vid := v.Wrap()
	vid.SetTxStatusGood(nil, 0)
	d.AddVertexNoLock(vid)
	return vid
}

func SavePastConeFromTxStore(tip ledger.TransactionID, txStore global.TxBytesGet, oldestSlot ledger.Slot, fname string) {
	tmpDag := MakeDAGFromTxStore(txStore, oldestSlot, tip)
	tmpDag.SaveGraph(fname)
}
