package ledger

import (
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
	"github.com/lunfardo314/unitrie/common"
)

type Amounts []uint64

const AmountsConstraintName = "amounts"

const (
	AmountIndexTokenBalance = byte(iota)
	AmountIndexInflation
	AmountIndexLockedCoverage
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

func _checkInflation(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output) {
	if !ctx.SelfIsProducedOutput() {
		// only check on produced outputs
		return
	}
	inflationGiven := o.Amounts().InflationAmount()
	if inflationGiven == 0 {
		// nothing to enforce
		return
	}
	// inflation > 0
	cc, idx := o.ChainConstraint()
	par.Require(idx != 0xff, "inflation must be 0 on non-chain output")

	txid := ctx.TransactionID()
	// produced, chain-constrained output with non-zero inflation value
	if txid.IsBranchTransaction() {
		_, stemIdx := ctx.SequencerAndStemOutputIndices()
		pathToStemLock := common.Concat(PathToProducedOutputs, stemIdx, ConstraintIndexLock)
		stemLockData, err := ctx.BytesAtPath(pathToStemLock)
		par.RequireNoError(err)
		stemLock, err := StemLockFromBytes(stemLockData)
		par.RequireNoError(err)

		bibCalc := L().BranchInflationBonusDirect(stemLock.VRFProof)
		par.Require(inflationGiven == bibCalc, "wrong branch inflation bonus value: expected %d (vrfProof=%s), got %d",
			bibCalc, hex.EncodeToString(stemLock.VRFProof), inflationGiven)
		return
	}
	// non-branch
	par.Require(!cc.IsOrigin(), "inflation must be 0 at chain origin")
	pathToPredecessorInput := common.Concat(PathToInputIDs, cc.PredecessorInputIndex)
	inputIDData, err := ctx.BytesAtPath(pathToPredecessorInput)
	par.RequireNoError(err)
	inputID, err := base.OutputIDFromBytes(inputIDData)
	par.RequireNoError(err)
	predTimestamp := inputID.Timestamp()

	pathToPredecessorOutput := common.Concat(PathToConsumedOutputs, cc.PredecessorInputIndex)
	predBytes, err := ctx.BytesAtPath(pathToPredecessorOutput)
	par.RequireNoError(err)
	predOutput, err := OutputFromBytes(predBytes)
	par.RequireNoError(err)

	inflationCalculated := L().CalcChainInflationAmountDirect(predTimestamp, txid.Timestamp(), predOutput.TokenBalance())
	par.Require(inflationGiven == inflationCalculated, "wrong inflation amount. Expected %d, got %d", inflationCalculated, inflationGiven)
}

func _checkLockedCoverage(par *easyfl.CallParams[*EvalContext], ctx *EvalContext, o *Output) {
	if !ctx.SelfIsProducedOutput() {
		// only check on produced outputs
		return
	}
	//dconst := DelegationConstants()
	//amounts := o.Amounts()
	//frozenCoverage := o.Amounts()[AmountIndexLockedCoverage: int(AmountIndexLockedCoverage)+dconst.MaxFrozenEpochs]

	// TODO make chain produced output
}

func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
	path := par.DataContext().EvalPath()
	par.Require(path[len(path)-1] == ConstraintIndexAmounts, "'amounts' must be at index %d", ConstraintIndexAmounts)
	ctx := par.DataContext()
	o := ctx.SelfOutput()
	_checkMinimumStorageDeposit(par, ctx, o)
	_checkInflation(par, ctx, o)
	_checkLockedCoverage(par, ctx, o)
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

// $0 'amounts' constraint bytecode
// Returns amount value 8 bytes from the output at path given in $0
func _tokenBalanceByOutputPath : 
if(
   isZero(parseNumArgs($0)),
   u64/0,
   uint8Bytes(parseInlineDataArgument($0, #amounts,0))
)

// $0 path to output
func tokenBalanceByOutputPath : _tokenBalanceByOutputPath(atPath(concat($0, amountsConstraintIndex)))

func selfTokenBalanceValue: tokenBalanceByOutputPath(selfOutputPath)

// $0 number of output bytes
func storageDeposit : mul(constVBCost16,$0)

// enforces storage deposit
func enforceMinimumStorageDeposit: 
	require(
		not(lessThan(selfTokenBalanceValue, storageDeposit(len(selfOutputBytes)))),
		!!!amount_on_output_is_smaller_than_allowed_minimum
	)
`
