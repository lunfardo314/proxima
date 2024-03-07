package glb

import (
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/lines"
)

func FileMustNotExist(dir string) {
	_, err := os.Stat(dir)
	if err == nil {
		Fatalf("'%s' already exists", dir)
	} else {
		if !os.IsNotExist(err) {
			AssertNoError(err)
		}
	}
}

func FileMustExist(dir string) {
	_, err := os.Stat(dir)
	AssertNoError(err)
}

func FileExists(name string) bool {
	_, err := os.Stat(name)
	return !os.IsNotExist(err)
}

func LinesOutputsWithIDs(outs []*ledger.OutputWithID, prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	for i, o := range outs {
		ln.Add("%d: %s", i, o.String())
	}
	return ln
}
