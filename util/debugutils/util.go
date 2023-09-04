package debugutils

import (
	"os"

	"github.com/dominikbraun/graph"
	"github.com/dominikbraun/graph/draw"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

type UTXOTangleWithDraftVertices struct {
	UTXOTangle    *utangle.UTXOTangle
	DraftVertices []*utangle.WrappedTx
}

func PanicWithLogFile(log *zap.SugaredLogger, fname string, graphSource any, format string, args ...any) {
	SaveGraph(fname, graphSource)
	if len(args) == 0 {
		if log != nil {
			log.Panicf(format, args...)
		} else {
			panic(format)
		}
	}
	//f, err := os.OpenFile(fname,
	//	os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	//util.Assertf(err == nil, "PanicWithLogFile: error while opening log file %s: '%v'", fname, err)
	//defer func() {
	//	_ = f.Close()
	//}()
	//_, _ = fmt.Fprintf(f, format, args...)
	//if log != nil {
	//	log.Panicf(format, args...)
	//} else {
	//	panic(fmt.Sprintf(format, args...))
	//}
}

func SaveGraph(fname string, src any) {
	if util.IsNil(src) {
		return
	}
	var gr graph.Graph[string, string]
	switch graphSource := src.(type) {
	case *utangle.WrappedTx:
		gr = utangle.MakeGraphPastCone(graphSource, 500)
	case *utangle.UTXOTangle:
		gr = graphSource.MakeGraph()
	case *UTXOTangleWithDraftVertices:
		gr = graphSource.UTXOTangle.MakeGraph(graphSource.DraftVertices...)
	default:
		panic("graph source must be *VertexPastCone, *UTXOTangle or *UTXOTangleWithDraftVertices")
	}
	dotFile, _ := os.Create(fname + ".gv")
	err := draw.DOT(gr, dotFile)
	util.AssertNoError(err)
	_ = dotFile.Close()
}
