package multistate

import (
	"os"
	"strconv"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
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

type (
	MakeTreeOptions struct {
		slotsBack            int
		branchNodeAttributes func(br *BranchData, seqNr int) []func(*graph.VertexProperties)
		chainIDDictionary    map[base.ChainID]int
		fname                string
	}

	MakeTreeOption func(opts *MakeTreeOptions)
)

func MakeTreeOptionWithSlotsBack(slotsBack int) MakeTreeOption {
	util.Assertf(slotsBack > 0, "slotsBack must be > 0")
	return func(opts *MakeTreeOptions) {
		opts.slotsBack = slotsBack
	}
}

func MakeTreeOptionWithFileName(fname string) MakeTreeOption {
	return func(opts *MakeTreeOptions) {
		opts.fname = fname
	}
}

func (opt *MakeTreeOptions) getSeqNr(seqID base.ChainID) int {
	var nr int
	var ok bool
	if nr, ok = opt.chainIDDictionary[seqID]; !ok {
		nr = len(opt.chainIDDictionary)
		opt.chainIDDictionary[seqID] = nr
	}
	return (nr % 8) + 1
}

func defaultMakeTreeOptions() *MakeTreeOptions {
	return &MakeTreeOptions{
		chainIDDictionary:    make(map[base.ChainID]int),
		branchNodeAttributes: branchNodeAttributesDefault,
		fname:                "tree.gv",
	}
}

func MakeTreeOpt(stateStore global.Store, options ...MakeTreeOption) graph.Graph[string, string] {
	opts := defaultMakeTreeOptions()
	for _, option := range options {
		option(opts)
	}

	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	var branches []*BranchData
	if opts.slotsBack == 0 {
		branches = FetchBranchDataMulti(stateStore, FetchAllRootRecords(stateStore)...)
	} else {
		branches = FetchBranchDataMulti(stateStore, FetchRootRecordsNSlotsBack(stateStore, opts.slotsBack)...)
	}

	byOid := make(map[base.OutputID]*BranchData)
	for _, b := range branches {
		byOid[b.Stem.ID] = b
		txid := b.Stem.ID.TransactionID()
		id := txid.StringShort()
		err := ret.AddVertex(id, opts.branchNodeAttributes(b, opts.getSeqNr(b.SequencerID))...)
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

func SaveBranchTreeOpt(stateStore global.Store, options ...MakeTreeOption) {
	opts := defaultMakeTreeOptions()
	for _, option := range options {
		option(opts)
	}
	fname := opts.fname

	gr := MakeTreeOpt(stateStore, options...)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func SaveBranchTree(stateStore global.Store, fname string, slotsBack ...int) {
	if len(slotsBack) == 0 {
		SaveBranchTreeOpt(stateStore, MakeTreeOptionWithFileName(fname))
	} else {
		SaveBranchTreeOpt(stateStore, MakeTreeOptionWithFileName(fname), MakeTreeOptionWithSlotsBack(slotsBack[0]))
	}
}

func branchNodeAttributesDefault(br *BranchData, seqNr int) []func(*graph.VertexProperties) {
	ret := make([]func(*graph.VertexProperties), len(_branchNodeAttributes))
	copy(ret, _branchNodeAttributes)
	ret = append(ret, graph.VertexAttribute("fillcolor", strconv.Itoa(seqNr)))
	if br.CoverageDelta > 0 {
		seqIDPref := br.SequencerID.StringHex()[:4]
		ret = append(ret, graph.VertexAttribute("xlabel", util.Th(br.CoverageDelta)+"-"+seqIDPref))
	}
	return ret
}
