package ledger

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type Amounts []uint64

const AmountsConstraintName = "amounts"

const (
	AmountIndexTokenBalance = byte(iota)
	AmountIndexInflation
	AmountIndexFrozenCoverage
)

func NewAmounts(args ...uint64) Amounts {
	util.Assertf(len(args) <= 15, "NewAmounts: too many arguments")
	lastNotZero := -1
	for i, arg := range args {
		if arg > 0 {
			lastNotZero = i
		}
	}
	if lastNotZero == -1 {
		return nil
	}
	return slices.Clone(args[:lastNotZero+1])
}

func (a Amounts) Name() string {
	return AmountsConstraintName
}

func (a Amounts) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a Amounts) Source() string {
	if len(a) == 0 {
		return AmountsConstraintName
	}
	argsStr := make([]string, len(a))
	for i, arg := range a {
		if arg == 0 {
			argsStr[i] = "0x"
		} else if arg <= 255 {
			argsStr[i] = strconv.FormatUint(arg, 10)
		} else {
			argsStr[i] = "z64/" + strconv.FormatUint(arg, 10)
		}
	}
	return AmountsConstraintName + "(" + strings.Join(argsStr, ",") + ")"
}

func (a Amounts) String() string {
	argsStr := make([]string, len(a))
	for i, arg := range a {
		argsStr[i] = util.Th(arg)
	}
	return AmountsConstraintName + "(" + strings.Join(argsStr, ",") + ")"
}

func (a Amounts) Amount(i byte) (ret uint64) {
	util.Assertf(i <= 15, "amount index is out of range: %d", i)
	if int(i) < len(a) {
		ret = a[i]
	}
	return
}

func (a Amounts) TokenBalance() (ret uint64) {
	ret = a.Amount(AmountIndexTokenBalance)
	return
}

func (a Amounts) InflationAmount() (ret uint64) {
	ret = a.Amount(AmountIndexInflation)
	return
}

func (a Amounts) FrozenCoverage() []uint64 {
	dconst := DelegationConstants()
	ret := make([]uint64, dconst.MaxFrozenEpochs)
	for i := 0; i < int(dconst.MaxFrozenEpochs); i++ {
		ret[i] = a.Amount(AmountIndexFrozenCoverage + byte(i))
	}
	return ret
}

// AddToVector adds amounts to vector with safe arithmetics
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process overflow
func (a Amounts) AddToVector(vect *[16]uint64) (overflow bool) {
	for i, v := range a {
		if vect[i] >= math.MaxUint64-v {
			overflow = true
		}
		vect[i] += v
	}
	return
}

func AmountsFromBytes(data []byte) (Amounts, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data)
	if err != nil {
		return nil, err
	}
	if sym != AmountsConstraintName {
		return nil, fmt.Errorf("AmountsFromBytes: not 'amounts' constraint")
	}
	ret := make(Amounts, len(args))
	for i, arg := range args {
		if ret[i], err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(arg)); err != nil {
			return nil, fmt.Errorf("AmountsFromBytes: %w", err)
		}
	}
	return ret, nil
}

func registerAmountsConstraint(lib *Library) {
	lib.mustRegisterVarargConstraint(AmountsConstraintName, func(data []byte) (Constraint, error) {
		return AmountsFromBytes(data)
	}, initTestAmountsConstraint)
}

func initTestAmountsConstraint() {
	example := NewAmounts(1, 2, 1337, 0x01020304050607)

	exampleBack, err := ConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.Name() == AmountsConstraintName, "inconsistency 1")
	exampleBack1 := exampleBack.(Amounts)
	util.Assertf(len(exampleBack1) == 4, "inconsistency 2")

	util.Assertf(exampleBack1[0] == 1, "exampleBack1[0]==1")
	util.Assertf(exampleBack1[1] == 2, "exampleBack1[1]==2")
	util.Assertf(exampleBack1[2] == 1337, "exampleBack1[2]==1337")
	util.Assertf(exampleBack1[3] == 0x01020304050607, "exampleBack1[3]==0x01020304050607")
}

const vByteCost = 1

func storageDepositByOutputBytes(data []byte) uint64 {
	return vByteCost * uint64(len(data))
}

var _locksExemptOfStorageDeposit = set.New(StemLockName)

func _checkMinimumStorageDeposit(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output) {
	if !ctx.SelfIsProducedOutput() {
		// only check on produced outputs
		return
	}
	if _locksExemptOfStorageDeposit.Contains(o.Lock().Name()) {
		return
	}
	bal := o.Amounts().TokenBalance()
	deposit := storageDepositByOutputBytes(ctx.SelfOutputBytes())
	par.Require(bal >= deposit, "token balance (%d) is less than required storage deposit (%d)", bal, deposit)
}

// _checkInflation The inflation constraint on the amount(1) is fully checked by the 'chain' constraint.
// The 'amounts' constraint only ensures, that amount(1)==0 if 'chain' constraint is not present
func _checkInflation(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output) {
	if !ctx.SelfIsProducedOutput() {
		// only check on produced outputs
		return
	}
	if o.Amounts().InflationAmount() == 0 {
		// nothing to enforce
		return
	}
	// inflation > 0
	_, idx := o.ChainConstraint()
	par.Require(idx != 0xff, "inflation must be 0 on non-chain output")
}

func _checkFrozenCoverage(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output) {
	if !ctx.SelfIsProducedOutput() {
		// only check on produced outputs
		return
	}
	amounts := o.Amounts()
	frozenCoverage := amounts.FrozenCoverage()
	if o.Lock().Name() == Delegate2LockName {
		// delegation output -> all constraints are checked by the delegate2 lock
		return
	}
	if _, idx := o.SequencerConstraint(); idx != 0xff {
		// sequencer output
		_checkLockedCoverageOnSequencer(par, ctx, o, frozenCoverage)
		return
	}
	if !util.AllZero(frozenCoverage...) {
		par.TracePanic("non-zero frozen coverage %s requires either '%s' lock or '%s' constraint on the output",
			amounts.String(), Delegate2LockName, SequencerConstraintName)
	}
	return
}

func _checkLockedCoverageOnSequencer(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output, frozenCoverage []uint64) {
	cc, idx := o.ChainConstraint()
	par.Require(!cc.IsOrigin(), "_checkLockedCoverageOnSequencer: unexpected chain origin")
	seqID := cc.ID
	par.Require(idx != 0xff, "_checkLockedCoverageOnSequencer: inconsistency 1")
	predID, err := ctx.InputID(cc.PredecessorInputIndex)
	par.RequireNoError(err)
	predTs := predID.Timestamp()
	txid := ctx.TransactionID()
	succTs := txid.Timestamp()

	dcons := DelegationConstants()
	predEpoch := dcons.EpochFromSlot(seqID, uint32(predTs.Slot))
	succEpoch := dcons.EpochFromSlot(seqID, uint32(succTs.Slot))
	par.Require(predEpoch <= succEpoch, "_checkLockedCoverageOnSequencer: inconsistency 2")
	diffEpochs := succEpoch - predEpoch

	predOut, err := ctx.ConsumedOutput(cc.PredecessorInputIndex)
	par.RequireNoError(err)

	predFrozenCoverage := predOut.Amounts().FrozenCoverage()

	deltaFrozen := make([]uint64, dcons.MaxFrozenEpochs)
	if diffEpochs < dcons.MaxFrozenEpochs {
		for i := diffEpochs; i < dcons.MaxFrozenEpochs; i++ {
			deltaFrozen[i] = predFrozenCoverage[i+diffEpochs]
		}
	}
	for i := 0; i < int(dcons.MaxFrozenEpochs); i++ {
		par.Require(deltaFrozen[i] <= frozenCoverage[i], "inconsistency: deltaFrozen[i]<frozenCoverage")
		deltaFrozen[i] = frozenCoverage[i] - deltaFrozen[i]
	}
	// TODO sums must match
}

func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	par.Require(path[len(path)-1] == ConstraintIndexAmounts, "'amounts' must be at index %d", ConstraintIndexAmounts)
	ctx := par.DataContext()
	o := ctx.SelfOutput()
	_checkMinimumStorageDeposit(par, ctx, o)
	_checkInflation(par, ctx, o)
	_checkFrozenCoverage(par, ctx, o)
	return []byte{0xff}
}

func evalTotalConsumed(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(par.DataContext().ConsumedTotal(idxBin[0]))
	return par.AllocData(ret[:]...)
}

func evalTotalProduced(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(par.DataContext().ProducedTotal(idxBin[0]))
	return par.AllocData(ret[:]...)
}

const amountsAuxSource = `
// $0 - 'amounts'' bytecode
// $1 - index of 'amounts' vector element, 1 byte
func amountAt:
if(
   lessThan($1, parseNumArgs($0)),
   uint8Bytes(parseInlineDataArgument($0, #amounts,$1)),
   u64/0
)

// $0 path to output
func tokenBalanceByOutputPath : amountAt(atPath(concat($0, amountsConstraintIndex)), 0)

// $0 amounts index
func selfAmountAt : amountAt(selfSiblingConstraint(amountsConstraintIndex),$0)

func selfTokenBalanceValue: selfAmountAt(0)

// $0 number of output bytes
func storageDeposit : mul(constVBCost16,$0)

// enforces storage deposit
func enforceMinimumStorageDeposit: 
	require(
		not(lessThan(selfTokenBalanceValue, storageDeposit(len(selfOutputBytes)))),
		!!!amount_on_output_is_smaller_than_allowed_minimum
	)
`
