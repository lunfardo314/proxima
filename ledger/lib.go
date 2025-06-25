package ledger

import (
	"crypto/ed25519"
	"encoding/binary"
	"math"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
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

	LibraryConst struct {
		*Library
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

func (lib *Library) Const() LibraryConst {
	return LibraryConst{lib}
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

// Library constants

func (lib LibraryConst) TicksPerSlot() byte {
	bin, err := lib.EvalFromSource(nil, "ticksPerSlot")
	util.AssertNoError(err)
	return bin[0]
}

func (lib LibraryConst) MinimumAmountOnSequencer() uint64 {
	bin, err := lib.EvalFromSource(nil, "constMinimumAmountOnSequencer")
	util.AssertNoError(err)
	ret := binary.BigEndian.Uint64(bin)
	util.Assertf(ret < math.MaxUint32, "ret < math.MaxUint32")
	return ret
}

func (lib LibraryConst) TicksPerInflationEpoch() uint64 {
	bin, err := lib.EvalFromSource(nil, "ticksPerInflationEpoch")
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(bin)
}
