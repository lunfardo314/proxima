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
	stemTemplate = StemLockName + "(u64/%d, %d, 0x%s, %d)"
)

const stemLockSource = `

// $0 predecessor input index
func predecessorSupply :
    unwrapBytecodeArg(
       consumedLockByInputIndex($0),
       selfBytecodePrefix,  
       0,   
    )

// $0 - supply
// $1 - predecessor input index
// $2 - predecessor output ID
// $3 - inflation constraint index on the sequencer output 
// unlock parameters is 1 byte of the successor stem output
func stemLock: and(
	require(isBranchTransaction, !!!must_be_a_branch_transaction),
    require(equal(selfNumConstraints, 2), !!!stem_output_must_contain_exactly_2_constraints),
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	require(isZero(selfAmountValue), !!!amount_must_be_zero),
	require(isZero(txTimeTick), !!!time_tick_must_be_0),
	mustSize($0, 8),
	mustSize($1, 1),
    require(
        equal(
           $0,
           sum64(
              predecessorSupply($1),
              inflationAmount($3),
           )
        ),
        !!!supply_on_stem_lock_is_not_consistent_with_inflation_and_previous_supply  
    ),
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
                 equal($2, inputIDByIndex($1)), 
                 !!!parameter_#2_must_be_equal_to_predecessor_input_ID
            ) 
		)
	)
)

`

type (
	StemOutputData struct {
		Supply                   uint64
		InflationConstraintIndex byte // index of the constraint on the sequencer output
	}

	StemLock struct {
		StemOutputData
		PredecessorIdx      byte
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
	return fmt.Sprintf(stemTemplate, st.Supply, st.PredecessorIdx, hex.EncodeToString(st.PredecessorOutputID[:]), st.InflationConstraintIndex)
}

func (st *StemLock) Bytes() []byte {
	return mustBinFromSource(st.source())
}

func (st *StemLock) String() string {
	return fmt.Sprintf("stem(%s, %d, %s, %d)",
		util.GoThousands(st.Supply), st.PredecessorIdx, st.PredecessorOutputID.String(), st.InflationConstraintIndex)
}

func (st *StemLock) Accounts() []Accountable {
	return []Accountable{st}
}

func (st *StemLock) UnlockableWith(_ AccountID, _ ...LogicalTime) bool {
	return true
}

func NewStemLock(supply uint64, predecessorInputIndex byte, predecessorOutputID OutputID, inflationConstraintIndex byte) *StemLock {
	return &StemLock{
		StemOutputData: StemOutputData{
			Supply:                   supply,
			InflationConstraintIndex: inflationConstraintIndex,
		},
		PredecessorIdx:      predecessorInputIndex,
		PredecessorOutputID: predecessorOutputID,
	}
}

func initStemLockConstraint() {
	easyfl.MustExtendMany(stemLockSource)
	// sanity check
	predID := NewOutputID(&TransactionID{}, 42)
	example := NewStemLock(10_000_000_000, 1, predID, 4)
	stem, err := StemLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(stem.Supply == 10_000_000_000 && stem.PredecessorIdx == 1 && stem.PredecessorOutputID == predID && stem.InflationConstraintIndex == 4,
		"'stem' consistency check failed")
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
	supplyBin := easyfl.StripDataPrefix(args[0])
	predIdxBin := easyfl.StripDataPrefix(args[1])
	predIDBin := easyfl.StripDataPrefix(args[2])
	inflationIdxBin := easyfl.StripDataPrefix(args[3])
	if len(supplyBin) != 8 || len(predIdxBin) != 1 || len(predIDBin) != OutputIDLength || len(inflationIdxBin) != 1 {
		return nil, fmt.Errorf("wrong data length")
	}
	oid, err := OutputIDFromBytes(predIDBin)
	if err != nil {
		return nil, err
	}

	return &StemLock{
		StemOutputData: StemOutputData{
			Supply:                   binary.BigEndian.Uint64(supplyBin),
			InflationConstraintIndex: inflationIdxBin[0],
		},
		PredecessorIdx:      predIdxBin[0],
		PredecessorOutputID: oid,
	}, nil
}
