package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	stemTemplate = StemLockName + "(u64/%d, u64/%d, 0x%s)"
)

type (
	StemLock struct {
		Supply              uint64
		InflationAmount     uint64
		PredecessorOutputID OutputID
	}
)

var StemAccountID = AccountID([]byte{0})

func (st *StemLock) AccountID() AccountID {
	return StemAccountID
}

func (st *StemLock) AsLock() Lock {
	return st
}

func (st *StemLock) Name() string {
	return StemLockName
}

func (st *StemLock) source() string {
	return fmt.Sprintf(stemTemplate, st.Supply, st.InflationAmount, hex.EncodeToString(st.PredecessorOutputID[:]))
}

func (st *StemLock) Bytes() []byte {
	return mustBinFromSource(st.source())
}

func (st *StemLock) String() string {
	return fmt.Sprintf("stem(%s, %s, %s)", util.GoTh(st.Supply), util.GoTh(st.InflationAmount), st.PredecessorOutputID.StringShort())
}

func (st *StemLock) Accounts() []Accountable {
	return []Accountable{st}
}

func (st *StemLock) UnlockableWith(_ AccountID, _ ...Time) bool {
	return true
}

func initStemLockConstraint() {
	easyfl.MustExtendMany(stemLockSource)
	// sanity check
	predID := NewOutputID(&TransactionID{}, 42)
	example := StemLock{
		Supply:              10_000_000_000,
		InflationAmount:     510,
		PredecessorOutputID: predID,
	}
	stem, err := StemLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(stem.Supply == 10_000_000_000, "stem.Supply == 10_000_000_000")
	util.Assertf(stem.InflationAmount == 510, "stem.InflationAmount == 314")
	util.Assertf(stem.PredecessorOutputID == predID, "stem.PredecessorOutputID == predID")
	prefix, err := easyfl.ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)
	registerConstraint(StemLockName, prefix, func(data []byte) (Constraint, error) {
		return StemLockFromBytes(data)
	})
}

func StemLockFromBytes(data []byte) (*StemLock, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 3)
	if err != nil {
		return nil, err
	}
	if sym != StemLockName {
		return nil, fmt.Errorf("not a 'stem' constraint")
	}
	predSupplyBin := easyfl.StripDataPrefix(args[0])
	inflationAmountBin := easyfl.StripDataPrefix(args[1])
	predIDBin := easyfl.StripDataPrefix(args[2])

	if len(predSupplyBin) != 8 || len(inflationAmountBin) != 8 || len(predIDBin) != OutputIDLength {
		return nil, fmt.Errorf("wrong data length")
	}
	oid, err := OutputIDFromBytes(predIDBin)
	if err != nil {
		return nil, err
	}

	return &StemLock{
		Supply:              binary.BigEndian.Uint64(predSupplyBin),
		InflationAmount:     binary.BigEndian.Uint64(inflationAmountBin),
		PredecessorOutputID: oid,
	}, nil
}

const stemLockSource = `

func _producedStem : lockConstraint(producedOutputByIndex(txStemOutputIndex))
func _supply : unwrapBytecodeArg(_producedStem, selfBytecodePrefix, 0)
func _inflation : unwrapBytecodeArg(_producedStem, selfBytecodePrefix, 1)
func _predOutputID : unwrapBytecodeArg(_producedStem, selfBytecodePrefix, 2)

// $0 - supply u64/ (must be predecessor supply + inflation)
// $1 - inflation amount u64/
// $2 - predecessor output ID
// does not require unlock parameters
func stemLock: and(
	require(isBranchTransaction, !!!must_be_a_branch_transaction),
    require(equal(selfNumConstraints, 2), !!!stem_output_must_contain_exactly_2_constraints),
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	require(isZero(selfAmountValue), !!!amount_must_be_zero),
	require(isZero(txTimeTick), !!!time_tick_must_be_0),
	mustSize($0, 8),
	mustSize($1, 8),
	mustSize($2, 33),
    if(
        selfIsConsumedOutput,
		and(
            require(equal(sum64($0, _inflation), _supply), !!!inconsistent_produced_inflation_with_produced_supply),
            require(equal(inputIDByIndex(selfOutputIndex), _predOutputID), !!!wrong_predecessor_output_ID)
        ),
		equal(selfOutputIndex, txStemOutputIndex),
    )
)

func txInflationAmount : 
    if(
        isBranchTransaction,
		unwrapBytecodeArg(
		   @Array8(producedOutputByIndex(txStemOutputIndex), lockConstraintIndex),
		   #stemLock,  
		   1,   
		),
        u64/0
    )
`
