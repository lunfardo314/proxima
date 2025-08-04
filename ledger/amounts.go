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
		if arg != 0 {
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

func (a Amounts) FrozenCoverageAt(i byte) (ret int64) {
	util.Assertf(uint32(i) < DelegationConst().MaxFrozenEpochs, "Amounts.FrozenCoverageAt: wrong vector index %d", i)
	return a.Amount(AmountIndexFrozenCoverage + i)
}

func (a Amounts) FrozenCoverageVector() []int64 {
	ret := make([]int64, DelegationConst().MaxFrozenEpochs)
	for i := range ret {
		ret[i] = a.FrozenCoverageAt(byte(i))
	}
	return ret
}

// AddToVector adds amounts to vector with safe arithmetics
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process overflow
func (a Amounts) AddToVector(vect *[15]int64) (overflow bool) {
	for i, v := range a {
		if v >= 0 {
			if vect[i] >= math.MaxInt64-v {
				overflow = true
			}
		} else {
			if vect[i] < math.MinInt64-v {
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

// TODO in the future it makes sense to rewrite it all in EasyFL, for formal verifiability with TLA model

func _checkInflation(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output, predAmounts Amounts, predSlot base.Slot) {
	var expectedInflation uint64
	inflation := o.Inflation()

	// inflation must be either 0 or exactly expected non-zero value
	if inflation == 0 {
		return
	}

	if ctx.IsBranchTransaction() {
		_, stemIdx := ctx.MustSequencerAndStemOutputIndices()
		stemOut, err := ctx.ProducedOutputWithIDAt(stemIdx)
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

// _checkFrozenCoverageOnNonDelegationChain assumes sequencer output and enforces the validity of the frozen coverage values
func _checkFrozenCoverageOnNonDelegationChain(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, seqID base.ChainID, amounts, predAmounts Amounts, txTs, predTs base.LedgerTime) {
	dcons := DelegationConst()
	diffEpochsInt := dcons.DiffEpochs(seqID, txTs, predTs)
	par.Require(diffEpochsInt >= 0, "_checkFrozenCoverageOnNonDelegationChain: inconsistency with timestamps")
	diffEpochs := uint32(diffEpochsInt)

	// frozen coverage at the predecessor adjusted to the epoch of the successor
	predecessorFrozenCoverageAdjusted := func(i uint32) (ret int64) {
		if idx := i + diffEpochs; idx < dcons.MaxFrozenEpochs {
			ret = predAmounts.FrozenCoverageAt(byte(idx))
		}
		return
	}

	// Enforce correct frozen coverage on sequencer output.
	// the validity constraint of frozen coverage on the chain at index i:
	// pred_i - value of the predecessor's frozen coverage at index i adjusted for the epoch difference between input and transaction
	// succ_i - value of the successor's (current output) frozen coverage at index i
	// delta_i (aux variable) - sum of frozen coverages (deltas, effectively) of produced delegation outputs at index i (not the target chain)
	// sum_i  - sum of ALL frozen coverages of produced outputs at index i
	// The equations:
	//    pred_i + delta_i = succ_i
	//    succ_i + delta_i = sum_i
	// leads to elimination of delta_i and final enforced validity constraint:
	//    pred_i + sum_i = 2 x succ_i

	for i := 0; i < int(dcons.MaxFrozenEpochs); i++ {
		successorFrozenCoverage := amounts.FrozenCoverageAt(byte(i))
		predecessorFrozenCoverageValue := predecessorFrozenCoverageAdjusted(uint32(i))
		sum := ctx.ProducedTotal(byte(i + 2))

		par.Require(2*successorFrozenCoverage == sum+predecessorFrozenCoverageValue,
			"_checkFrozenCoverageOnNonDelegationChain: mismatch between frozen coverage totals at index %d: predCov=%d, succCov=%d, delta=%d, producedSum=%d",
			i, predecessorFrozenCoverageValue, successorFrozenCoverage, successorFrozenCoverage-predecessorFrozenCoverageValue, sum)
	}
}

// _checkFrozenCoverageOnDelegateOutput assumes produced, not-origin delegation output. Enforces correct frozen coverage values
func _checkFrozenCoverageOnDelegateOutput(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o, predOut *Output, succID, predID base.OutputID) {
	dOut, ok := AsDelegateOutput(o, succID)
	par.Require(ok, "_checkFrozenCoverageOnDelegateOutput: inconsistency, delegation output expectedVector 1")
	amounts := o.Amounts()

	if ctx.IsUnlockedBy(dOut.MasterLock) {
		// transition by the master -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(),
			"_checkFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to reason: unlocked by the master in delegation chain %s", dOut.ChainID.String())
		return
	}
	// unlocked by the target as enforced by the delegation lock
	var expectedVector []int64
	// the expected vector is different for frozen and revoked delegation outputs
	if dOut.Revoked {
		pred, err := ctx.ConsumedOutput(dOut.PredecessorInputIndex)
		par.RequireNoError(err)
		dOutPred, ok := AsDelegateOutput(pred, ctx.MustInputAt(dOut.PredecessorInputIndex))
		par.Require(ok, "_checkFrozenCoverageOnDelegateOutput: delegation output expectedVector at predecessor")

		// the expected vector contains negative deltas of revoked frozen coverage in the current transaction (adjusted to the epoch difference)
		expectedVector = dOutPred.MakeFrozenCoverageAmountDeltasForRevoking(ctx.Timestamp())
	} else {
		frozenEpochs, err := dOut.FrozenEpochs(ctx.Timestamp())
		par.RequireNoError(err)

		// the expected vector contains frozen coverages for the span of the frozen epochs
		expectedVector, err = dOut.MakeFrozenCoverageAmounts(frozenEpochs, dOut.Output.TokenBalance())
		par.RequireNoError(err)
	}

	vectorToCheck := o.Amounts().FrozenCoverageVector()
	par.Require(len(expectedVector) == len(vectorToCheck), "len(expectedVector) == len(vectorToCheck)")
	par.Require(slices.Equal(expectedVector, vectorToCheck), "_checkFrozenCoverageOnDelegateOutput: wrong frozen coverage value in delegation chain %s", dOut.ChainID.String)
}

// DelegateLock is a special case in amounts and inflation validation

func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	par.Require(path[len(path)-1] == ConstraintIndexAmounts, "'amounts' must be at index %d", ConstraintIndexAmounts)
	ctx := par.DataContext()
	o := ctx.SelfOutput()
	_checkMinimumStorageDeposit(par, ctx, o)
	if !ctx.SelfIsProducedOutput() {
		// only enforce the validity of amounts on produced outputs
		return []byte{0xff}
	}
	amounts := o.Amounts()
	cc, _ := o.ChainConstraint()
	// produced output
	if cc == nil || cc.IsOrigin() {
		par.Require(o.Inflation() == 0 && amounts.IsFrozenCoverageZero(), "evalAmounts: inflation and frozen coverage must be 0 on a non-chain output")
		return []byte{0xff}
	}
	// it is a non-origin chain output

	predOut, err := ctx.ConsumedOutput(cc.PredecessorInputIndex)
	par.RequireNoError(err)
	predAmounts := predOut.Amounts()

	predID := ctx.MustInputAt(cc.PredecessorInputIndex)
	succID := ctx.OutputID(path[len(path)-2])

	// check inflation:
	// TODO on frozen delegation
	_checkInflation(par, ctx, o, predAmounts, predID.Slot())

	if o.Lock().Name() == DelegateLockName {
		_checkFrozenCoverageOnDelegateOutput(par, ctx, o, predOut, succID, predID)
	} else {
		_checkFrozenCoverageOnNonDelegationChain(par, ctx, cc.ChainID, amounts, predAmounts, succID.Timestamp(), predID.Timestamp())
	}

	return []byte{0xff}
}

func evalTotalConsumed(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(uint64(par.DataContext().ConsumedTotal(idxBin[0])))
	return par.AllocData(ret[:]...)
}

func evalTotalProduced(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(uint64(par.DataContext().ProducedTotal(idxBin[0])))
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
