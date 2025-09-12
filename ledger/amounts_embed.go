package ledger

import (
	"slices"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// TODO proper implementation of the storage deposit

const vByteCost = 1

func StorageDepositByOutputBytes(data []byte) uint64 {
	return vByteCost * uint64(len(data))
}

var _locksExemptOfStorageDeposit = set.New(StemLockName)

func _enforceMinimumStorageDeposit(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output) {
	if !ctx.SelfIsProducedOutput() {
		// only check on produced outputs
		return
	}
	if _locksExemptOfStorageDeposit.Contains(o.Lock().Name()) {
		return
	}
	bal := o.Amounts().TokenBalance()
	deposit := StorageDepositByOutputBytes(ctx.SelfOutputBytes())
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
		expectedInflation = BranchInflationBonus(stemLock.VRFProof)

		par.Require(expectedInflation == inflation, "evalAmounts: wrong branch inflation bonus. Expected %s, got %s",
			util.Th(expectedInflation), util.Th(inflation))
	} else {
		if predSlot != ctx.Timestamp().Slot {
			inAmount := predAmounts.Amount(AmountIndexTokenBalance)
			// do not inflate frozen coverage on delegation output, otherwise standard one-slot inflation
			if o.Lock().Name() != DelegateLockName {
				inAmount += predAmounts.Amount(AmountIndexFrozenCoverage)
			}
			expectedInflation = ChainInflationOneSlot(uint64(inAmount), uint32(predSlot))
		}
		par.Require(expectedInflation == inflation, "evalAmounts: wrong chain inflation value. Expected %s, got %s",
			util.Th(expectedInflation), util.Th(inflation))
	}
}

// _checkFrozenCoverageOnNonDelegationChain assumes sequencer output and enforces the validity of the frozen coverage values
func _checkFrozenCoverageOnNonDelegationChain(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, seqID base.ChainID, amounts, predAmounts Amounts, txTs, predTs base.LedgerTime) {
	diffEpochsInt := Const.DiffEpochs(seqID, txTs, predTs)
	par.Require(diffEpochsInt >= 0, "_checkFrozenCoverageOnNonDelegationChain: inconsistency with timestamps")
	diffEpochs := uint32(diffEpochsInt)

	// frozen coverage at the predecessor adjusted to the epoch of the successor
	predecessorFrozenCoverageAdjusted := func(i uint32) (ret int64) {
		if idx := i + diffEpochs; idx < Const.MaxFrozenEpochs {
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

	for i := 0; i < int(Const.MaxFrozenEpochs); i++ {
		successorFrozenCoverage := amounts.FrozenCoverageAt(byte(i))
		predecessorFrozenCoverageValue := predecessorFrozenCoverageAdjusted(uint32(i))
		sum := ctx.ProducedTotal(byte(i + 2))

		par.Require(2*successorFrozenCoverage == sum+predecessorFrozenCoverageValue,
			"_checkFrozenCoverageOnNonDelegationChain: mismatch between frozen coverage totals at index %d: predCov=%d, succCov=%d, delta=%d, producedSum=%d",
			i, predecessorFrozenCoverageValue, successorFrozenCoverage, successorFrozenCoverage-predecessorFrozenCoverageValue, sum)
	}
}

// _checkFrozenCoverageOnDelegateOutput assumes produced, not-origin delegation output. Enforces correct frozen coverage values
func _checkFrozenCoverageOnDelegateOutput(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output, succID base.OutputID) {
	dOut, ok := AsDelegationOutput(o, succID)
	par.Require(ok, "_checkFrozenCoverageOnDelegateOutput: inconsistency, delegation output expectedVector 1")
	amounts := o.Amounts()

	pred, err := ctx.ConsumedOutput(dOut.PredecessorInputIndex)
	par.RequireNoError(err)

	if pred.Lock().Name() != DelegateLockName {
		// predecessor is not delegation -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(),
			"_checkFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to the reason: chain predecessor is not a delegation")
		return
	}
	// predecessor is delegation
	// unlock parameters of predecessor delegation lock must be 3 bytes
	unlock, err := par.DataContext().UnlockParameters(dOut.PredecessorInputIndex, ConstraintIndexLock)
	par.RequireNoError(err)
	par.Require(len(unlock) >= 3, "_checkFrozenCoverageOnDelegateOutput: unlock parameters of predecessor delegation lock at (%d, %d) must be 3 bytes",
		dOut.PredecessorInputIndex, ConstraintIndexLock)

	if unlock[2] == DelegationUnlockedByMaster {
		// predecessor is delegation unlocked by master  -> must be all-0
		par.Require(amounts.IsFrozenCoverageZero(),
			"_checkFrozenCoverageOnDelegateOutput: expectedVector all-0 frozen coverage due to the reason: predecessor is unlocked by the master")
		return
	}

	// unlocked by the target as enforced by the delegation lock
	var expectedVector []int64
	// the expected vector is different for frozen and revoked delegation outputs
	if dOut.State == DelegateLockStateOnHold {
		dOutPred, ok := AsDelegationOutput(pred, ctx.MustInputAt(dOut.PredecessorInputIndex))
		par.Require(ok, "_checkFrozenCoverageOnDelegateOutput: delegation output expectedVector at predecessor")

		// the expected vector contains negative deltas of revoked frozen coverage in the current transaction (adjusted to the epoch difference)
		expectedVector = dOutPred.MakeFrozenCoverageAmountDeltasForRevoking(ctx.Timestamp())
	} else {
		_, _, frozenEpochs := dOut.FrozenEpochs(ctx.Timestamp())
		par.Require(frozenEpochs <= 256, "inconsistency: frozenEpochs <= 256")
		// the expected vector contains frozen coverages for the span of the frozen epochs
		expectedVector, err = dOut.MakeFrozenCoverageAmounts(byte(frozenEpochs), dOut.Output.TokenBalance())
		par.RequireNoError(err)
	}

	vectorToCheck := o.Amounts().FrozenCoverageVector()
	par.Require(len(expectedVector) == len(vectorToCheck), "len(expectedVector) == len(vectorToCheck)")
	par.Require(slices.Equal(expectedVector, vectorToCheck), "_checkFrozenCoverageOnDelegateOutput: wrong frozen coverage value in delegation output: %s", dOut.ChainID.String)
}

// DelegateLock is a special case in amounts and inflation validation

func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	par.Require(path[len(path)-1] == ConstraintIndexAmounts, "'amounts' must be at index %d", ConstraintIndexAmounts)
	ctx := par.DataContext()
	o := ctx.SelfOutput()
	_enforceMinimumStorageDeposit(par, ctx, o)
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
		_checkFrozenCoverageOnDelegateOutput(par, ctx, o, succID)
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
