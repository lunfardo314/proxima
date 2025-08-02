package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type Amounts []int64

const AmountsConstraintName = "amounts"

const (
	AmountIndexTokenBalance = byte(iota)
	AmountIndexInflation
	AmountIndexFrozenCoverage
)

func NewAmounts(args ...int64) Amounts {
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
		if 0 < arg && arg <= 255 {
			// one byte
			argsStr[i] = strconv.FormatInt(arg, 10)
		} else {
			// zero-trimmed 8 uint64 bytes
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(arg))
			trimmed := easyfl_util.TrimLeadingZeroBytes(b[:])
			argsStr[i] = "0x" + hex.EncodeToString(trimmed)
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

func (a Amounts) Amount(i byte) (ret int64) {
	util.Assertf(i <= 15, "amount index is out of range: %d", i)
	if int(i) < len(a) {
		ret = a[i]
	}
	return
}

func (a Amounts) TokenBalance() uint64 {
	ret := a.Amount(AmountIndexTokenBalance)
	util.Assertf(ret >= 0, "token balance can't be negative. Got %d", a)
	return uint64(ret)
}

func (a Amounts) InflationAmount() uint64 {
	ret := a.Amount(AmountIndexInflation)
	util.Assertf(ret >= 0, "inflation amount must can't be negative. Got %d", a)
	return uint64(ret)
}

func (a Amounts) IsFrozenCoverageZero() bool {
	dconst := DelegationConst()
	for i := 0; i < int(dconst.MaxFrozenEpochs); i++ {
		if a.Amount(AmountIndexFrozenCoverage+byte(i)) != 0 {
			return false
		}
	}
	return true
}

func (a Amounts) FrozenCoverage(i byte) (ret int64) {
	util.Assertf(uint32(i) < DelegationConst().MaxFrozenEpochs, "Amounts.FrozenCoverage: wrong vector index %d", i)
	return a.Amount(AmountIndexFrozenCoverage + i)
}

// AddToVector adds amounts to vector with safe arithmetics
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process overflow
func (a Amounts) AddToVector(vect *[16]int64) (overflow bool) {
	for i, v := range a {
		if v >= 0 {
			if vect[i] >= math.MaxInt64-v {
				overflow = true
			}
		} else {
			if vect[i] < -v {
				overflow = true
			}
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
	var v uint64
	for i, arg := range args {
		v, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(arg))
		if err != nil {
			return nil, fmt.Errorf("AmountsFromBytes: %w", err)
		}
		ret[i] = int64(v)
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

// TODO in the future it makes sense to rewrite it all in EasyFL

func _checkInflation(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output, predAmounts Amounts, predSlot base.Slot) {
	var expectedInflation uint64
	inflation := o.Inflation()

	// inflation must be either 0 or exactly expected non-zero value
	if inflation == 0 {
		return
	}

	if ctx.IsBranchTransaction() {
		_, stemIdx := ctx.SequencerAndStemOutputIndices()
		stemOut, err := ctx.ProducedOutput(stemIdx)
		par.RequireNoError(err)
		stemLock, ok := stemOut.Output.StemLock()
		par.Require(ok, "inconsistency: can't find stemLock")
		expectedInflation = L().BranchInflationBonusDirect(stemLock.VRFProof)

		par.Require(expectedInflation == inflation, "evalAmounts: wrong branch inflation bonus. Expected %s, got %s",
			util.Th(expectedInflation), util.Th(inflation))
	} else {
		if predSlot != ctx.Timestamp().Slot {
			inAmount := predAmounts.Amount(AmountIndexTokenBalance)
			// do not inflate frozen coverage on delegation output, otherwise standard one-slot inflation
			if o.Lock().Name() != DelegateLockName {
				inAmount += predAmounts.Amount(AmountIndexFrozenCoverage)
			}
			expectedInflation = L().CalcChainInflationAmountOneSlot(predSlot, uint64(inAmount))
		}
		par.Require(expectedInflation == inflation, "evalAmounts: wrong chain inflation value. Expected %s, got %s",
			util.Th(expectedInflation), util.Th(inflation))
	}
}

func _checkFrozenCoverageOnSequencer(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, seqID base.ChainID, amounts, predAmounts Amounts, txSlot, predSlot base.Slot) {
	// sequencer output
	dcons := DelegationConst()
	predEpoch := dcons.EpochFromSlot(seqID, uint32(predSlot))
	succEpoch := dcons.EpochFromSlot(seqID, uint32(txSlot))
	par.Require(predEpoch <= succEpoch, "evalAmounts: inconsistency 1")

	// adjustment to the difference between epochs of predecessor and successor
	diffEpochs := byte(succEpoch - predEpoch)
	// frozen coverage at the predecessor adjusted to the epoch of the successor
	// if diffEpochs >= dconst.MaxFrozenEpochs it will always be 0
	predFrozenCoverageAdjusted := func(i byte) (ret int64) {
		if i >= diffEpochs {
			ret = predAmounts.Amount(AmountIndexFrozenCoverage + i - diffEpochs)
		}
		return
	}
	// Enforce correct frozen coverage on sequencer output.
	// Must be: 2*successorFrozenCoverage == sumOfFrozenDelegations + predecessorFrozenCoverageAdjusted
	for i := 0; i < int(dcons.MaxFrozenEpochs); i++ {
		idx := AmountIndexFrozenCoverage + byte(i)
		successorFrozenCoverage := amounts.Amount(idx)
		predecessorFrozenCoverageAdjusted := predFrozenCoverageAdjusted(byte(i))
		par.Require(successorFrozenCoverage >= predecessorFrozenCoverageAdjusted, "inconsistency 3 at index %d", i)
		sum := ctx.ProducedTotal(idx)
		par.Require(2*successorFrozenCoverage == int64(sum)+predecessorFrozenCoverageAdjusted,
			"_checkFrozenCoverageOnSequencer: mismatch between frozen coverage totals at index %d: predCov=%d, succCov=%d, delta=%d, producedSum=%d",
			i, predecessorFrozenCoverageAdjusted, successorFrozenCoverage, successorFrozenCoverage-predecessorFrozenCoverageAdjusted, sum)
	}
}

// DelegateLock is a special case in amounts and inflation validation

func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	par.Require(path[len(path)-1] == ConstraintIndexAmounts, "'amounts' must be at index %d", ConstraintIndexAmounts)
	ctx := par.DataContext()
	o := ctx.SelfOutput()
	_checkMinimumStorageDeposit(par, ctx, o)
	cc, _ := o.ChainConstraint()
	if !ctx.SelfIsProducedOutput() {
		// only enforce amount on produced outputs
		return []byte{0xff}
	}
	amounts := o.Amounts()
	// produced output
	if cc == nil || cc.IsOrigin() {
		par.Require(o.Inflation() == 0 && amounts.IsFrozenCoverageZero(), "evalAmounts: inflation and frozen coverage must be 0 on a non-chain output")
		return []byte{0xff}
	}

	predID, err := ctx.InputID(cc.PredecessorInputIndex)
	par.RequireNoError(err)
	txid := ctx.TransactionID()
	predOut, err := ctx.ConsumedOutput(cc.PredecessorInputIndex)
	par.RequireNoError(err)
	predAmounts := predOut.Amounts()

	// check inflation:
	_checkInflation(par, ctx, o, predAmounts, predID.Slot())

	if o.Lock().Name() == DelegateLockName {
		// delegation output -> frozen coverage constraints are enforced by the delegate2 lock
		// TODO move it here?
		return []byte{0xff}
	}

	// check frozen coverage
	if _, idx := o.SequencerConstraint(); idx != 0xff {
		// check frozen coverage on sequencer
		_checkFrozenCoverageOnSequencer(par, ctx, cc.ID, amounts, predAmounts, txid.Slot(), predID.Slot())
	} else {
		// only sequencer and delegation outputs can have non-zero frozen coverage
		par.Require(amounts.IsFrozenCoverageZero(), "evalAmounts: expected all-0 frozen coverage")
	}
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
