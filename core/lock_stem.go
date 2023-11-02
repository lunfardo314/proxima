package core

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	stemTemplate = StemLockName + "(%d, u64/%d, u64/%d, 0x%s)"
)

const stemLockSource = `

// $0 - predecessor input index
func predecessorSupply :
    unwrapBytecodeArg(
       consumedLockByInputIndex($0),
       selfBytecodePrefix,  
       1,   
    )

// $0 - stem predecessor input index
// $1 - supply u64/ (must be predecessor supply + inflation)
// $2 - inflation amount u64/
// $3 - predecessor output ID
// unlock parameters is 1 byte of the successor stem output
func stemLock: and(
	require(isBranchTransaction, !!!must_be_a_branch_transaction),
    require(equal(selfNumConstraints, 2), !!!stem_output_must_contain_exactly_2_constraints),
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	require(isZero(selfAmountValue), !!!amount_must_be_zero),
	require(isZero(txTimeTick), !!!time_tick_must_be_0),
	mustSize($0, 1),
	mustSize($1, 8),
	mustSize($2, 8),
	mustSize($3, 33),
	or(
		and(
			selfIsConsumedOutput,
			equal(selfUnlockParameters, txStemOutputIndex)
		),
		and(
			selfIsProducedOutput,
			equal(selfOutputIndex, txStemOutputIndex),
			isZero(unwrapBytecodeArg(selfSiblingConstraint(0), #amount, 0)), 
            require(
                 equal($3, inputIDByIndex($1)), 
                 !!!parameter_#3_must_be_equal_to_predecessor_input_ID
            ),
            require(
                 equal($1, sum64(predecessorSupply($0), $2)),
                 !!!total_supply_inconsistent_with_inflation
            ),
		)
	)
)

func txInflationAmount : 
    if(
        isBranchTransaction,
		unwrapBytecodeArg(
		   @Array8(producedOutputByIndex(txStemOutputIndex), lockConstraintIndex),
		   #stemLock,  
		   3,   
		),
        u64/0
    )

`

type (
	StemLock struct {
		Supply              uint64
		InflationAmount     uint64
		PredecessorOutputID OutputID
		StemPredecessorIdx  byte
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
	return fmt.Sprintf(stemTemplate,
		st.StemPredecessorIdx, st.Supply, st.InflationAmount, hex.EncodeToString(st.PredecessorOutputID[:]))
}

func (st *StemLock) Bytes() []byte {
	return mustBinFromSource(st.source())
}

func (st *StemLock) String() string {
	return fmt.Sprintf("stem(%d, %s, %s, %s)",
		st.StemPredecessorIdx, util.GoThousands(st.Supply),
		util.GoThousands(st.InflationAmount), st.PredecessorOutputID.Short())
}

func (st *StemLock) Accounts() []Accountable {
	return []Accountable{st}
}

func (st *StemLock) UnlockableWith(_ AccountID, _ ...LogicalTime) bool {
	return true
}

func initStemLockConstraint() {
	easyfl.MustExtendMany(stemLockSource)
	// sanity check
	predID := NewOutputID(&TransactionID{}, 42)
	example := StemLock{
		Supply:              10_000_000_000,
		InflationAmount:     314,
		PredecessorOutputID: predID,
		StemPredecessorIdx:  1,
	}
	stem, err := StemLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(stem.Supply == 10_000_000_000, "stem.Supply == 10_000_000_000")
	util.Assertf(stem.InflationAmount == 314, "stem.InflationAmount == 314")
	util.Assertf(stem.PredecessorOutputID == predID, "stem.PredecessorOutputID == predID")
	util.Assertf(stem.StemPredecessorIdx == 1, "stem.StemPredecessorIdx == 1")
	prefix, err := easyfl.ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)
	registerConstraint(StemLockName, prefix, func(data []byte) (Constraint, error) {
		return StemLockFromBytes(data)
	})
}

func StemLockFromBytes(data []byte) (*StemLock, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, err
	}
	if sym != StemLockName {
		return nil, fmt.Errorf("not a 'stem' constraint")
	}
	predIdxBin := easyfl.StripDataPrefix(args[0])
	predSupplyBin := easyfl.StripDataPrefix(args[1])
	inflationAmountBin := easyfl.StripDataPrefix(args[2])
	predIDBin := easyfl.StripDataPrefix(args[3])

	if len(predSupplyBin) != 8 ||
		len(inflationAmountBin) != 8 ||
		len(predIDBin) != OutputIDLength ||
		len(predIdxBin) != 1 {
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
		StemPredecessorIdx:  predIdxBin[0],
	}, nil
}
