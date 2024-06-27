package depdag

import (
	"os"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/util"
)

type Node struct {
	ID           string   `yaml:"id"`
	Dependencies []string `yaml:"dependencies"`
}

var (
	nodeAttr = []func(*graph.VertexProperties){
		graph.VertexAttribute("fontsize", "10"),
		graph.VertexAttribute("shape", "box"),
		graph.VertexAttribute("colorscheme", "blues3"),
		graph.VertexAttribute("style", "filled"),
		graph.VertexAttribute("color", "2"),
		graph.VertexAttribute("fillcolor", "1"),
	}
)

func MakeDAG(nodes []Node) graph.Graph[string, string] {
	ret := graph.New(graph.StringHash, graph.Directed(), graph.Acyclic())

	for _, wanted := range nodes {
		_ = ret.AddVertex(wanted.ID, nodeAttr...)
		for _, whoIsWaiting := range wanted.Dependencies {
			_ = ret.AddVertex(whoIsWaiting, nodeAttr...)
		}
	}

	for _, wanted := range nodes {
		for _, whoIsWaiting := range wanted.Dependencies {
			_ = ret.AddEdge(whoIsWaiting, wanted.ID)
		}
	}
	return ret
}

func SaveDAG(nodes []Node, fname string) {
	gr := MakeDAG(nodes)
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}
