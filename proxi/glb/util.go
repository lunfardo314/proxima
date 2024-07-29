package glb

import (
	"io"
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

func DirMustNotExistOrBeEmpty(dir string) {
	_, err := os.Stat(dir)
	if err == nil {
		// exists so must be empty
		empty, _ := isDirEmpty(dir)
		if !empty {
			Fatalf("'%s' is not empty", dir)
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

func isDirEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// Read at most one entry from the directory
	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}
