package ledger

import (
	"crypto/ed25519"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/testutil"
)

type (
	Library struct {
		*easyfl.Library[*EvalContext]
		definitionsYAML    []byte
		constraintByPrefix map[string]*constraintRecord
		constraintNames    set.Set[string]
		locksByName        map[string]LockParser
		inlineTests        []func()
	}
)

func newLibrary(lib *easyfl.Library[*EvalContext], definitionsYAML []byte) *Library {
	ret := &Library{
		Library:            lib,
		definitionsYAML:    definitionsYAML,
		constraintByPrefix: make(map[string]*constraintRecord),
		constraintNames:    set.New[string](),
		locksByName:        make(map[string]LockParser),
		inlineTests:        make([]func(), 0),
	}
	return ret
}

func newBaseLibrary() *Library {
	return newLibrary(easyfl.NewBaseLibrary[*EvalContext](), nil)
}

// DefinitionsYAML returns the compiled library YAML definitions.
func (lib *Library) DefinitionsYAML() []byte {
	if len(lib.definitionsYAML) > 0 {
		return lib.definitionsYAML
	}
	return lib.Library.ToYAML(true, "# Proxima library upgraded from EasyFL base")
}

func GetTestingLedgerParams(seed ...int) (InitParameters, ed25519.PrivateKey) {
	s := 10000
	for _, i := range seed {
		s += i
	}

	pk := testutil.GetTestingPrivateKey(s)
	return DefaultParameters(pk, uint32(time.Now().Unix())), pk
}
