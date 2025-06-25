package ledger

import (
	"crypto/ed25519"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util/testutil"
)

type (
	Library struct {
		*easyfl.Library[*EvalContext]
		idData             []byte
		ID                 *IdentityParameters
		constraintByPrefix map[string]*constraintRecord
		constraintNames    map[string]struct{}
		inlineTests        []func()
	}
)

func newLibrary(lib *easyfl.Library[*EvalContext], idParams *IdentityParameters, idData []byte) *Library {
	ret := &Library{
		Library:            lib,
		idData:             idData,
		ID:                 idParams,
		constraintByPrefix: make(map[string]*constraintRecord),
		constraintNames:    make(map[string]struct{}),
		inlineTests:        make([]func(), 0),
	}
	return ret
}

func newBaseLibrary(id *IdentityParameters) *Library {
	return newLibrary(easyfl.NewBaseLibrary[*EvalContext](), id, nil)
}

func (lib *Library) IdentityData() []byte {
	if len(lib.idData) > 0 {
		return lib.idData
	}
	return lib.Library.ToYAML(true, "# Proxima library upgraded from EasyFL base")
}

func GetTestingIdentityData(seed ...int) (*IdentityParameters, ed25519.PrivateKey) {
	s := 10000
	for _, i := range seed {
		s += i
	}

	pk := testutil.GetTestingPrivateKey(s)
	return DefaultIdentityParameters(pk, uint32(time.Now().Unix())), pk
}
