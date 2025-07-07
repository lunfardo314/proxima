package ledger

import (
	"bytes"
	"encoding/binary"
	"math/big"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// EvalContext is the data structure passed to the eval call. It contains:
// - access interface to the transaction context
// - path: a path in the validation context of the constraint being validated in the eval call
type (
	TxContextAccess interface {
		BytesAtPath([]byte) ([]byte, error)
		TransactionID() base.TransactionID
		SequencerAndStemOutputIndices() (byte, byte)
	}

	EvalContext struct {
		TxContextAccess
		path []byte
	}
)

func NewEvalContext(ctx TxContextAccess) *EvalContext {
	return &EvalContext{
		TxContextAccess: ctx,
	}
}

func (c *EvalContext) TxContext() TxContextAccess {
	return c.TxContextAccess
}

func (c *EvalContext) SelfIsConsumedOutput() bool {
	return bytes.HasPrefix(c.path, PathToConsumedOutputs)
}

func (c *EvalContext) SelfIsProducedOutput() bool {
	return bytes.HasPrefix(c.path, PathToProducedOutputs)
}

func (c *EvalContext) SelfOutputBytes() (ret []byte) {
	var err error
	ret, err = c.BytesAtPath(c.path[:len(c.path)-1])
	util.AssertNoError(err)
	return
}

func (c *EvalContext) SelfOutput() *Output {
	ret, err := OutputFromBytes(c.SelfOutputBytes())
	util.AssertNoError(err)
	return ret
}

func (c *EvalContext) SelfSiblingPath(idx byte) (ret []byte) {
	ret = bytes.Clone(c.path)
	ret[len(ret)-1] = idx
	return
}

func (c *EvalContext) SelfSiblingBytes(idx byte) (ret []byte) {
	var err error
	ret, err = c.BytesAtPath(c.SelfSiblingPath(idx))
	util.AssertNoError(err)
	return
}

func (c *EvalContext) EvalPath() []byte {
	return c.path
}

func (c *EvalContext) SetEvalPath(path []byte) {
	c.path = bytes.Clone(path)
}

var _unboundedEmbedded = map[string]easyfl.EmbeddedFunction[*EvalContext]{
	"at":             evalPath,
	"atPath":         evalAtPath,
	"amounts":        evalAmounts,
	"ticksBefore":    evalTicksBefore64, // TODO make it extended in pure EasyFL
	"randomFromSeed": evalRandomFromSeed,
}

func GetEmbeddedFunctionResolver(lib *easyfl.Library[*EvalContext]) func(sym string) easyfl.EmbeddedFunction[*EvalContext] {
	baseResolver := easyfl.EmbeddedFunctions(lib)
	return func(sym string) easyfl.EmbeddedFunction[*EvalContext] {
		if ret, found := _unboundedEmbedded[sym]; found {
			return ret
		}
		return baseResolver(sym)
	}
}

// EmbedHardcoded upgrades library with hardcoded (embedded) functions of the proxima ledger
func EmbedHardcoded(lib *easyfl.Library[*EvalContext]) error {
	return lib.UpgradeFromYAML([]byte(_definitionsEmbeddedYAML), GetEmbeddedFunctionResolver(lib))
}

const _definitionsEmbeddedYAML string = `
functions:
# short
   -
      sym: "at"
      description: "returns path in the transaction of the validity constraint being evaluated"
      numArgs: 0
      embedded: true
      short: true
   -
      sym: "atPath"
      description: "returns element of the transaction at path $0"
      numArgs: 1
      embedded: true
      short: true
# long
   -
      sym: "amounts"
      description: "UTXO constraint for the vector of amounts"
      numArgs: -1
      embedded: true
   -
      sym: ticksBefore
      description: "number of ticks between timestamps $0 and $1 as big-endian uint64 if $0 is before $1, or 0x otherwise"
      numArgs: 2
      embedded: true
   -
      sym: randomFromSeed
      description: "uses $0 as seed to deterministically calculate a pseudo-random value. Returns 8-byte big-endian integer bytes in the interval [0,$1)"
      numArgs: 2
      embedded: true
`

// embedded functions

func evalPath(par *easyfl.CallParams[*EvalContext]) []byte {
	return par.AllocData(par.DataContext().EvalPath()...)
}

func evalAtPath(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.Arg(0)
	res, err := par.DataContext().BytesAtPath(path)
	if err != nil {
		par.TracePanic("evalAtPath: path=%+v -> %v", path, err)
	}
	return par.AllocData(res...)
}

func evalRandomFromSeed(par *easyfl.CallParams[*EvalContext]) []byte {
	data := par.Arg(0)
	scale := easyfl_util.MustUint64FromBytes(par.Arg(1))

	var rnd uint64
	err := util.CatchPanicOrError(func() error {
		rnd = RandomFromSeed(data, scale)
		return nil
	})
	if err != nil {
		par.Trace("'randomFromSeed embedded' failed with: %v", err)
		return nil
	}
	ret := par.Alloc(8)
	binary.BigEndian.PutUint64(ret, rnd)
	return ret
}

// arg 0 and arg 1 are timestamps (5 bytes each)
// returns:
// nil, if ts1 is before ts0
// number of ticks between ts0 and ts1 otherwise, as big-endian uint64
func evalTicksBefore64(par *easyfl.CallParams[*EvalContext]) []byte {
	ts0bin, ts1bin := par.Arg(0), par.Arg(1)
	ts0, err := base.LedgerTimeFromBytes(ts0bin)
	if err != nil {
		par.TracePanic("evalTicksBefore64: %v", err)
	}
	ts1, err := base.LedgerTimeFromBytes(ts1bin)
	if err != nil {
		par.TracePanic("evalTicksBefore64: %v", err)
	}
	diff := base.DiffTicks(ts1, ts0)
	if diff < 0 {
		// ts1 is before ts0
		return nil
	}
	ret := par.Alloc(8)
	binary.BigEndian.PutUint64(ret, uint64(diff))
	return ret
}

// RandomFromSeed returns a random uin64 number in [0, scale) by scaling the data
// value as BigInt to the interval [0, scale). The 'scale' value itself is not included
// It is used to extract a verifiable random uint64 from a ED25519 signature.
func RandomFromSeed(data []byte, scale uint64) uint64 {
	h := blake2b.Sum256(data)
	ret := new(big.Int).SetBytes(h[:])
	ret.Mod(ret, new(big.Int).SetUint64(scale))
	return ret.Uint64()
}
