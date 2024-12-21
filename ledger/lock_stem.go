package ledger

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	stemTemplate = StemLockName + "(0x%s)"
)

type (
	StemLock struct {
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

func (st *StemLock) Source() string {
	return fmt.Sprintf(stemTemplate, hex.EncodeToString(st.PredecessorOutputID[:]))
}

func (st *StemLock) Bytes() []byte {
	return mustBinFromSource(st.Source())
}

func (st *StemLock) String() string {
	return st.Source()
	//return fmt.Sprintf("stem(%s)", st.PredecessorOutputID.StringShort())
}

func (st *StemLock) Accounts() []Accountable {
	return []Accountable{st}
}

func addStemLockConstraint(lib *Library) {
	lib.extendWithConstraint(StemLockName, stemLockSource, 1, func(data []byte) (Constraint, error) {
		return StemLockFromBytes(data)
	}, initTestStemLockConstraint)
}

func initTestStemLockConstraint() {
	txid := RandomTransactionID(true)
	predID := MustNewOutputID(&txid, byte(txid.NumProducedOutputs()-1))
	example := StemLock{
		PredecessorOutputID: predID,
	}
	stem, err := StemLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(stem.PredecessorOutputID == predID, "stem.PredecessorOutputID == predID")
	_, err = L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

func StemLockFromBytes(data []byte) (*StemLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != StemLockName {
		return nil, fmt.Errorf("not a 'stem' constraint")
	}
	predIDBin := easyfl.StripDataPrefix(args[0])

	oid, err := OutputIDFromBytes(predIDBin)
	if err != nil {
		return nil, err
	}

	return &StemLock{
		PredecessorOutputID: oid,
	}, nil
}

const stemLockSource = `
func producedStemLockOfSelfTx : lockConstraint(producedOutputByIndex(txStemOutputIndex))
func _predOutputID : evalArgumentBytecode(producedStemLockOfSelfTx, selfBytecodePrefix, 0)

// $0 - predecessor output ID
// does not require unlock parameters
func stemLock: and(
	require(isBranchTransaction, !!!must_be_a_branch_transaction),
    require(equal(selfNumConstraints, 2), !!!stem_output_must_contain_exactly_2_constraints),
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	require(isZero(selfAmountValue), !!!amount_must_be_zero),
	require(isZero(txTimeTick), !!!time_tick_must_be_0),
	mustSize($0, 33),
    if(
        selfIsConsumedOutput,
		require(equal(inputIDByIndex(selfOutputIndex), _predOutputID), !!!wrong_predecessor_output_ID),
		equal(selfOutputIndex, txStemOutputIndex),
    )
)

// utility function to get stem predecessor. Does not use 'selfBytecodePrefix''
func predStemOutputIDOfSelf : evalArgumentBytecode(producedStemLockOfSelfTx, #stemLock, 0)
`
