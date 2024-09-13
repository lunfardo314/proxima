package multistate

import (
	"os"
	"strconv"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

var (
	fontsizeAttribute = graph.VertexAttribute("fontsize", "10")

	_branchNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("colorscheme", "accent8"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "2"),
		graph.VertexAttribute("fillcolor", "1"),
	}
)

func MakeTree(stateStore global.StateStore, slots ...int) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	var branches []*BranchData
	if len(slots) == 0 {
		branches = FetchBranchDataMulti(stateStore, FetchAllRootRecords(stateStore)...)
	} else {
		branches = FetchBranchDataMulti(stateStore, FetchRootRecordsNSlotsBack(stateStore, slots[0])...)
	}

	byOid := make(map[ledger.OutputID]*BranchData)
	idDict := make(map[ledger.ChainID]int)
	for _, b := range branches {
		byOid[b.Stem.ID] = b
		txid := b.Stem.ID.TransactionID()
		id := txid.StringShort()
		err := ret.AddVertex(id, branchNodeAttributes(&b.SequencerID, b.LedgerCoverage, idDict)...)
		util.AssertNoError(err)
	}

	for _, b := range branches {
		txid := b.Stem.ID.TransactionID()
		id := txid.StringShort()
		stemLock, stemLockFound := b.Stem.Output.StemLock()
		util.Assertf(stemLockFound, "stem lock not found")

		if pred, ok := byOid[stemLock.PredecessorOutputID]; ok {
			txid := pred.Stem.ID.TransactionID()
			predID := txid.StringShort()
			err := ret.AddEdge(id, predID)
			util.AssertNoError(err)
		}
	}
	return ret
}

func SaveBranchTree(stateStore global.StateStore, fname string, slotsBack ...int) {
	gr := MakeTree(stateStore, slotsBack...)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func branchNodeAttributes(seqID *ledger.ChainID, coverage uint64, dict map[ledger.ChainID]int) []func(*graph.VertexProperties) {
	if _, found := dict[*seqID]; !found {
		dict[*seqID] = (len(dict) % 9) + 1
	}
	ret := make([]func(*graph.VertexProperties), len(_branchNodeAttributes))
	copy(ret, _branchNodeAttributes)
	ret = append(ret, graph.VertexAttribute("fillcolor", strconv.Itoa(dict[*seqID])))
	if coverage > 0 {
		seqIDPref := seqID.StringHex()[:4]
		ret = append(ret, graph.VertexAttribute("xlabel", util.Th(coverage)+"-"+seqIDPref))
	}
	return ret
}
