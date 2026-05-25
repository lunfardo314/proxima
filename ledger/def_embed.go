package ledger

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
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
		SubtreeAtPath([]byte) (*tuples.Tree, error)
		ConsumedOutput(idx byte) (*Output, error)
		ConsumedTotal(i byte) int64
		ProducedTotal(i byte) int64
		IsBranchTransaction() bool
		IsSequencerTransaction() bool
		ID() base.TransactionID
		NumInputs() int
		NumProducedOutputs() int
		ProducedOutputAt(idx byte) (*Output, error)
		ProducedOutputWithIDAt(idx byte) (*OutputWithID, error)
		Timestamp() base.LedgerTime
		MustInputAt(idx byte) base.OutputID
		OutputID(idx byte) base.OutputID
		SequencerTransactionData() *SequencerTransactionData
		HolderID() (base.HolderID, error)
		UnlockParameters(inputIdx, constraintIdx byte) ([]byte, error)
		GetLibrary() *Library
		// IsScriptRedeemed reports whether a local-script hash has been
		// committed by a prior redeemScript constraint in this tx.
		IsScriptRedeemed(h [32]byte) bool
		// AddRedeemedScript records a local-script hash committed by a
		// redeemScript constraint. Idempotent.
		AddRedeemedScript(h [32]byte)
		// NativeTokenAggregator returns the per-tx per-tag native-token
		// aggregator, allocating on first call. Populated lazily by the
		// first token() builtin call in the tx.
		NativeTokenAggregator() *NativeTokenAggregator
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
	// Uses latest library version - upgrade code must maintain backward-compatible parsing
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

// EmbeddedResolver resolves a symbol to an embedded function.
// Returns nil if the symbol is not in this resolver's scope.
type EmbeddedResolver func(string) easyfl.EmbeddedFunction[*EvalContext]

// upgradeEmbeddedResolvers is the static list of embedded function resolvers.
// Each entry represents an upgrade that adds new embedded functions.
// Entries are in ascending slot order. Upgrades with only pure EasyFL formulas
// don't need an entry here.
var upgradeEmbeddedResolvers []struct {
	Slot     uint32
	Resolver EmbeddedResolver
}

func init() {
	upgradeEmbeddedResolvers = []struct {
		Slot     uint32
		Resolver EmbeddedResolver
	}{
		{0, resolveEmbeddedUpgrade0},
		// Future upgrades that add embedded functions are added here.
		// Example: {100, resolveEmbeddedUpgrade1} for sum3
	}
}

// resolveEmbeddedUpgrade0 resolves embedded functions from upgrade 0.
func resolveEmbeddedUpgrade0(sym string) easyfl.EmbeddedFunction[*EvalContext] {
	if ret, found := _unboundedEmbedded[sym]; found {
		return ret
	}
	return nil
}

var _unboundedEmbedded = map[string]easyfl.EmbeddedFunction[*EvalContext]{
	"evalPath":                     evalPath,
	"evalAtPath":                   evalAtPath,
	"evalTotalConsumed":            evalTotalConsumed,
	"evalTotalProduced":            evalTotalProduced,
	"evalTicksBefore64":            evalTicksBefore64, // TODO make it in pure EasyFL
	"evalRandomFromSeed":           evalRandomFromSeed,
	"evalTxID":                     evalTxID,
	"evalTupleHasDuplicatesAtPath": evalTupleHasDuplicatesAtPath,
	"evalTupleLenAtPath":           evalTupleLenAtPath,
	"embeddedEnforceFrozenCoverageOnDelegateOutput":     evalEnforceFrozenCoverageOnDelegateOutput,
	"embeddedEnforceFrozenCoverageOnNonDelegationChain": evalEnforceFrozenCoverageOnNonDelegationChain,
	"embeddedDelegationOriginCrossCheck":                evalDelegationOriginCrossCheck,
	"embeddedIsInflationAndFrozenCoverageZero":          evalIsInflationAndFrozenCoverageZero,
	"evalRedeemScript":                                  evalRedeemScript,
	"evalCallRedeemer":                                  evalCallRedeemer,
	"evalToken":                                         evalToken,
	"evalTokenAmount":                                   evalTokenAmount,
	"evalBlake2b":                                       evalBlake2b,
	"evalValidSignatureED25519":                         evalValidSignatureED25519,
}

// GetEmbeddedFunctionResolver returns the unified resolver for all upgrades.
// It searches through upgrade resolvers in descending order (newest first),
// then falls back to the base easyfl resolver.
func GetEmbeddedFunctionResolver(lib *easyfl.Library[*EvalContext]) func(sym string) easyfl.EmbeddedFunction[*EvalContext] {
	baseResolver := easyfl.EmbeddedFunctions(lib)
	return func(sym string) easyfl.EmbeddedFunction[*EvalContext] {
		// Try each upgrade resolver in descending order (newest first)
		for i := len(upgradeEmbeddedResolvers) - 1; i >= 0; i-- {
			entry := upgradeEmbeddedResolvers[i]
			if entry.Resolver != nil {
				if ret := entry.Resolver(sym); ret != nil {
					return ret
				}
			}
		}
		// Fall back to base easyfl resolver
		if ret := baseResolver(sym); ret != nil {
			return ret
		}
		return func(glb *easyfl.CallParams[*EvalContext]) []byte {
			panic(fmt.Sprintf("inconsistency: embedded function symbol '%s' wasn't resolved properly", sym))
		}
	}
}

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

// deterministic pseudo-random uint64 value from seed scaled to $0 bytes.
// Used for extraction of value from VRF
func evalRandomFromSeed(par *easyfl.CallParams[*EvalContext]) []byte {
	data := par.Arg(0)
	scale := easyfl_util.MustUint64FromBytes(par.Arg(1))

	var rnd uint64
	err := util.CatchPanicOrError(func() error {
		rnd = RandomFromSeed(data, scale)
		return nil
	})
	if err != nil {
		return nil
	}
	ret := par.Alloc(8)
	binary.BigEndian.PutUint64(ret, rnd)
	return ret
}

func evalTxID(par *easyfl.CallParams[*EvalContext]) []byte {
	ret := par.DataContext().ID()
	return par.AllocData(ret[:]...)
}

func evalTupleLenAtPath(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.Arg(0)
	subtree, err := par.DataContext().SubtreeAtPath(path)
	if err != nil {
		par.TracePanic("evalTupleLenAtPath: path=%+v -> %v", path, err)
		return nil
	}
	ret := par.Alloc(8)
	binary.BigEndian.PutUint64(ret, uint64(subtree.Tuple.NumElements()))
	return ret
}

func evalTupleHasDuplicatesAtPath(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.Arg(0)
	subtree, err := par.DataContext().SubtreeAtPath(path)
	if err != nil {
		par.TracePanic("evalTupleHasDuplicatesAtPath: path=%+v -> %v", path, err)
		return nil
	}
	if subtree.Tuple.HasDuplicates() {
		return []byte{0xff}
	}
	return nil
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
