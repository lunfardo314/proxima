package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const sequencerConstraintSource = `
func mustMinimumAmountOnSequencer : 
	require(
 	 	 not(lessThan(selfTokenBalanceValue, constMinimumAmountOnSequencer)), 
		 !!!minimum_sequencer_amount_constraint_failed
	)

// $0 chain predecessor input index
func _inputSameSlot :
equal(
	txSlot,
	timeSlotOfInputByIndex($0)
)

// no param
// the tx is origin of both chain and sequencer
func _noChainPredecessorCase :
and(
	require(not(isBranchTransaction),     !!!sequencer_chain_origin_can't_be_on_branch_transaction),
	require(not(isZero(numEndorsements)), !!!sequencer_chain_origin_must_endorse_another_sequencer_transaction)
)

// $0 chain predecessor input index
// chain predecessor is on the same slot
func _sameSlotPredecessorCase : 
require( 
	or(sequencerFlagON(inputIDByIndex($0)), not(isZero(numEndorsements))),
	!!!sequencer_chain_predecessor_on_the_same_slot_must_be_either_a_sequencer_tx_too_or_endorse_another_sequencer_tx  
)

// $0 chain predecessor input index
// chain predecessor is on past time slot
func _crossSlotPredecessorCase : 
require(
	or(
		isBranchTransaction, 
		not(isZero(numEndorsements)),
		not(isZero(txExplicitBaseline))
	), 
	!!!sequencer_tx_has_incorrect_cross_slot_chain_predecessor_or_does_not_have_any_endorsements
)

// $0 chain predecessor input index
func _sequencer :
or(
	and( equal($0, 0xff), _noChainPredecessorCase ),
	and( _inputSameSlot($0), _sameSlotPredecessorCase($0)),
	and( not(_inputSameSlot($0)), _crossSlotPredecessorCase)
)

func zeroTickOnBranchOnly : or(
	not(isZero(txTick)),
	isBranchTransaction
)

// enforces the sequencer transaction with more than 1 input not is in the pre branch consolidation ticks zone
// Checks: txTick <= constMaxTickValuePerSlot - constPreBranchConsolidationTicks
func checkPreBranchConsolidationTicks :
or(
   equal(numInputs, u64/1),
   require(
		lessOrEqualThan(
			uint8Bytes(txTick),
			sub(
				constMaxTickValuePerSlot,
				constPreBranchConsolidationTicks
			)
        ),
        !!!sequencer_transaction_violates_pre-branch_consolidation_ticks_constraint
   )
)

func checkPostBranchConsolidationTicks :
   require(
       or(
          isBranchTransaction,
          lessOrEqualThan(constPostBranchConsolidationTicks, uint8Bytes(txTick))
	   ),
       !!!sequencer_transaction_violates_post_branch_consolidation_ticks_constraint
   )

// $0 is chain constraint sibling index
func sequencer: and(
	mustSize($0,1),
    mustMinimumAmountOnSequencer, // enforcing minimum amount on sequencer
    or(
        selfIsConsumedOutput,
		and(
			// produced
			require(not(equal(selfOutputIndex, 0xff)), !!!sequencer_output_can't_be_at_index_0xff),
			require(equal(selfOutputIndex, txSequencerOutputIndex), !!!inconsistent_sequencer_output_index_on_transaction),
			require(not(equal($0, 0xff)), !!!chain_constraint_index_0xff_is_not_allowed),
            require(zeroTickOnBranchOnly, !!!non-branch_sequencer_transaction_cant_be_on_slot_boundary), 
			checkPreBranchConsolidationTicks,
			checkPostBranchConsolidationTicks,
            // check chain's past
			_sequencer( selfChainPredInputIndex($0) )
		)
    )
)
`

const (
	SequencerConstraintName     = "sequencer"
	sequencerConstraintTemplate = SequencerConstraintName + "(%d)"
)

type (
	SequencerConstraint struct {
		// must point to the sibling chain constraint
		ChainConstraintIndex byte
	}
	// SequencerTransactionData represents sequencer and stem data on the transaction
	SequencerTransactionData struct {
		SequencerOutputData  *SequencerOutputData
		StemOutputData       *StemLock    // nil if does not contain stem output
		SequencerID          base.ChainID // adjusted for chain origin
		SequencerOutputIndex byte
		StemOutputIndex      byte // 0xff if not a branch transaction
	}
)

func (m *SequencerTransactionData) Short() string {
	return fmt.Sprintf("SEQ(%s)", m.SequencerID.StringVeryShort())
}

func NewSequencerConstraint(chainConstraintIndex byte) *SequencerConstraint {
	return &SequencerConstraint{
		ChainConstraintIndex: chainConstraintIndex,
	}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *SequencerConstraint) String() string {
	return fmt.Sprintf("%s(%d)", SequencerConstraintName, s.ChainConstraintIndex)
}

func (s *SequencerConstraint) Source() string {
	return fmt.Sprintf(sequencerConstraintTemplate, s.ChainConstraintIndex)
}

func SequencerConstraintFromBytes(data []byte) (*SequencerConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != SequencerConstraintName {
		return nil, fmt.Errorf("not a sequencerConstraintIndex")
	}
	cciBin := easyfl.StripDataPrefix(args[0])
	if len(cciBin) != 1 {
		return nil, fmt.Errorf("wrong chainConstraintIndex parameter")
	}
	cci := cciBin[0]

	return &SequencerConstraint{
		ChainConstraintIndex: cci,
	}, nil
}

func registerSequencerConstraint(lib *Library) {
	lib.mustRegisterConstraint(SequencerConstraintName, 1, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytes(data)
	}, initTestSequencerConstraint)
}

func initTestSequencerConstraint() {
	example := NewSequencerConstraint(4)
	sym, _, args, err := L().ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")

	cciBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(cciBin) == 1, "len(cciBin) == 1")
	util.Assertf(cciBin[0] == 4, "cciBin[0] == 4")
}
