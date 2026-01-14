package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	stemTemplate = StemLockName + "(0x%s,0x%s)"
)

type (
	StemLock struct {
		PredecessorOutputID base.OutputID
		VRFProof            []byte
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
	return fmt.Sprintf(stemTemplate,
		hex.EncodeToString(st.PredecessorOutputID[:]),
		hex.EncodeToString(st.VRFProof),
	)
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

func (st *StemLock) Master() Accountable {
	return nil
}

func registerStemLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(StemLockName, 2, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return StemLockFromBytesAtSlot(data, base.MaxSlot)
	}, initTestStemLockConstraint)
	lib.mustRegisterLock(StemLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := StemLockFromBytesAtSlot(bytes, base.MaxSlot)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestStemLockConstraint() {
	lib := L(base.MaxSlot)
	txid := base.RandomTransactionID(true, 1)
	predID := base.MustNewOutputID(txid, byte(txid.NumProducedOutputs()-1))
	example := StemLock{
		PredecessorOutputID: predID,
		VRFProof:            []byte{0x01, 0x02, 0x03},
	}
	exampleBack, err := StemLockFromBytesAtSlot(example.Bytes(), base.MaxSlot)
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(example.Bytes(), exampleBack.Bytes()), "bytes.Equal(example.Bytes(), exampleBack.Bytes())")
	_, err = lib.ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

// StemLockFromBytesWithLib parses a StemLock using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
func StemLockFromBytesWithLib(data []byte, lib *Library) (*StemLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != StemLockName {
		return nil, fmt.Errorf("not a 'stem' constraint")
	}
	oid, err := base.OutputIDFromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}

	return &StemLock{
		PredecessorOutputID: oid,
		VRFProof:            easyfl.StripDataPrefix(args[1]),
	}, nil
}

// StemLockFromBytesAtSlot parses a StemLock using the library for the given slot.
func StemLockFromBytesAtSlot(data []byte, slot uint32) (*StemLock, error) {
	return StemLockFromBytesWithLib(data, L(slot))
}

const stemLockSource = `
func producedStemLockOfSelfTx : lockConstraint(producedOutputByIndex(txStemOutputIndex))

func _predOutputIDOnSuccessor : parseInlineDataArgument(producedStemLockOfSelfTx, 0)
func _vrfProofOnSuccessor : parseInlineDataArgument(producedStemLockOfSelfTx, 1)

// $0 - stem predecessor index
func _predVRFProof : parseInlineDataArgument(
    consumedConstraintByIndex($0,1), 
    1, 
    selfBytecodePrefix
)

// $0 - predecessor output id
// $1 - VRF proof (ED25519 signature of concatenation of VRF proof from the stem predecessor and slot of the transaction)
// does not require unlock parameters
func stemLock: and(
	require(isBranchTransaction, !!!must_be_a_branch_transaction),
    require(equalUint(selfNumConstraints, 2), !!!stem_output_must_contain_exactly_2_constraints),
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	require(isZero(selfTokenBalanceValue), !!!amount_must_be_zero),
	mustSize($0, 33),
    or(
       and(
          selfIsConsumedOutput,
             // enforce correct predecessor output on the successor
          require(
             equal(inputIDByIndex(selfOutputIndex), _predOutputIDOnSuccessor), 
             !!!wrong_stem_predecessor_output_ID_on_successor
          ),
             // enforce correct VRF proof on successor
		  require(
             validSignatureED25519(concat($1, txSlot), _vrfProofOnSuccessor, publicKeyED25519(txSignature)), 
             !!!VRF_proof_check_failed
          )
       ),
       and(
          selfIsProducedOutput,
            // must be consistent with the transaction level data
          equal(selfOutputIndex, txStemOutputIndex)
       )
    )
)
`
