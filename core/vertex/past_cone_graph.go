package vertex

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

var (
	fontsizeAttribute = graph.VertexAttribute("fontsize", "10")
	//colorScheme       = graph.VertexAttribute("colorscheme", "spectral11")
	//colorScheme = graph.VertexAttribute("colorscheme", "rdylbu11")
	colorScheme = graph.VertexAttribute("colorscheme", "paired12")
	//colorScheme = graph.VertexAttribute("colorscheme", "prgn11")
	numColors = 9

	nilNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("shape", "circle"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("fillcolor", "black"),
	}
	simpleNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		colorScheme,
		graph.VertexAttribute("shape", "box"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("fillcolor", strconv.Itoa(numColors)),
	}
	seqNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		colorScheme,
		graph.VertexAttribute("shape", "ellipse"),
		graph.VertexAttribute("style", "filled"),
	}
	branchNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		colorScheme,
		graph.VertexAttribute("shape", "octagon"),
		graph.VertexAttribute("style", "filled"),
	}
	missingNodeAttributes = []func(*graph.VertexProperties){
		fontsizeAttribute,
		graph.VertexAttribute("shape", "box"),
		//graph.VertexAttribute("style", "filled"),
		//graph.VertexAttribute("fillcolor", "black"),
	}
)

const virtualConsumerName = "nil"

func (pc *PastCone) MakeGraph() graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	seqMap := make(map[ledger.ChainID]int)
	pc.forAllVertices(func(vid *WrappedTx) bool {
		pc.makeGraphNode(vid, ret, seqMap)
		return true
	})
	pc.forAllVertices(func(vid *WrappedTx) bool {
		pc.makeConsumerEdges(vid, ret)
		pc.makeEndorsementEdges(vid, ret)
		return true
	})
	return ret
}

func (pc *PastCone) SaveGraph(fname string) {
	gr := pc.MakeGraph()
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}

func fillColor(vid *WrappedTx, seqMap map[ledger.ChainID]int) func(*graph.VertexProperties) {
	seqID := vid.SequencerID.Load()
	if seqID == nil {
		return graph.VertexAttribute("fillcolor", strconv.Itoa(numColors))
	}
	ok := false
	if _, ok = seqMap[*seqID]; !ok {
		if len(seqMap)+2 > numColors {
			return graph.VertexAttribute("fillcolor", strconv.Itoa(numColors))
		} else {
			seqMap[*seqID] = len(seqMap) + 2
		}
	}
	return graph.VertexAttribute("fillcolor", strconv.Itoa(seqMap[*seqID]))
}

func vertexID(vid *WrappedTx) string {
	if vid == nil {
		return virtualConsumerName
	}
	return vid.ID.AsFileNameShort()
}

func (pc *PastCone) nodeAttributes(vid *WrappedTx, seqMap map[ledger.ChainID]int) (ret []func(*graph.VertexProperties)) {
	switch {
	case vid == nil:
		ret = slices.Clone(nilNodeAttributes)

	case vid.IsBranchTransaction():
		ret = slices.Clone(branchNodeAttributes)

	case vid.IsSequencerMilestone():
		ret = slices.Clone(seqNodeAttributes)

	default:
		ret = slices.Clone(simpleNodeAttributes)
	}
	ret = append(ret, fillColor(vid, seqMap))
	if pc.baseline == vid {
		ret = append(ret,
			graph.VertexAttribute("penwidth", "5"),
			graph.VertexAttribute("color", "red"),
		)
	} else {
		if pc.IsInTheState(vid) {
			ret = append(ret,
				graph.VertexAttribute("penwidth", "3"),
				//graph.VertexAttribute("color", "red"),
			)
		} else {
			ret = append(ret, graph.VertexAttribute("penwidth", "1"))
		}
	}
	return ret
}

func (pc *PastCone) makeGraphNode(vid *WrappedTx, gr graph.Graph[string, string], seqMap map[ledger.ChainID]int) {
	attr := pc.nodeAttributes(vid, seqMap)
	err := gr.AddVertex(vertexID(vid), attr...)
	pc.AssertNoError(err)
}

func (pc *PastCone) makeConsumerEdges(vid *WrappedTx, gr graph.Graph[string, string]) {
	for idx := range pc.consumersByOutputIndex(vid) {
		wOut := WrappedOutput{VID: vid, Index: idx}

		for _, consumer := range pc.findConsumersOf(wOut) {
			edgeAttributes := []func(_ *graph.EdgeProperties){
				graph.EdgeAttribute("label", fmt.Sprintf("#%d", idx)),
				graph.EdgeAttribute("fontsize", "10"),
			}
			err := gr.AddEdge(vertexID(consumer), vertexID(vid), edgeAttributes...)
			pc.AssertNoError(err, "makeConsumerEdges")
		}
	}
}

func (pc *PastCone) makeEndorsementEdges(vid *WrappedTx, gr graph.Graph[string, string]) {
	var endorsements []*WrappedTx
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			endorsements = v.Endorsements
		},
	})
	for _, vidEndorsed := range endorsements {
		edgeAttributes := []func(_ *graph.EdgeProperties){
			graph.EdgeAttribute("color", "red"),
			graph.EdgeAttribute("fontsize", "10"),
		}
		err := gr.AddEdge(vertexID(vid), vertexID(vidEndorsed), edgeAttributes...)
		if errors.Is(err, graph.ErrEdgeNotFound) {
			_ = gr.AddVertex(vertexID(vid), missingNodeAttributes...)
			_ = gr.AddVertex(vertexID(vidEndorsed), missingNodeAttributes...)
		}
	}
}
