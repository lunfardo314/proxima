package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// TODO TEMPORARY. Only for testing and experimenting. Constants must be different in reality
const (
	// MinimumAmountOnSequencer minimum amount of tokens on the sequencer's chain output
	MinimumAmountOnSequencer = 1_000_000 //
	// MaxInflationPerBranchFraction total amount on the predecessor is divided by this constant to get inflation amount on branch
	MaxInflationPerBranchFraction = 1_000_000
)

const (
	mustMinSeqAmountTemplate = `
	require(
 	 	 not(lessThan(selfAmountValue, u64/%d)), 
		 !!!minimum_sequencer_amount_constraint_failed
	)
	`
	validInflationTemplate = `
	// check if inflation amount is valid
	// $0 - total produced amount on predecessor. It does not matter for simple sequencer tx, it is equal 
	//    to total input amount of the branch tx   
	// $1 - inflation amount
	if(
		 isBranchTransaction,
		 require(lessOrEqualThan($1, div64($0, u64/%d)), !!!wrong_inflation_amount_on_branch_transaction),
		 require(isZero($1), !!!inflation_must_be_zero_on_non_branch_transaction)
	)
`
)

var (
	minimumAmountOnSeqSource = fmt.Sprintf(mustMinSeqAmountTemplate, MinimumAmountOnSequencer)
	validInflationSource     = fmt.Sprintf(validInflationTemplate, MaxInflationPerBranchFraction)
)

const sequencerConstraintSource = `
// $0 chain predecessor input index
func _inputSameSlot :
equal(
	txTimeSlot,
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
	!!!sequencer_chain_predecessor_on_the_same_time_slot_must_be_either_a_sequencer_tx_too_or_endorse_another_sequencer_tx  
)

// $0 chain predecessor input index
// chain predecessor is on past time slot
func _crossSlotPredecessorCase : 
require(
	or(
		//  and(isBranchTransaction, sequencerFlagON(inputIDByIndex($0))),  
        //  <<<<<<< modified Oct 4, 2023. Otherwise sequencer can't start from pure chain'
		isBranchTransaction, 
		not(isZero(numEndorsements))
	), 
	!!!sequencer_tx_has_incorrect_cross_slot_chain_predecessor_or_don't_have_any_endorsements
)

// $0 chain predecessor input index
func _sequencer :
or(
	and( equal($0, 0xff), _noChainPredecessorCase ),
	and( _inputSameSlot($0), _sameSlotPredecessorCase($0)),
	and( not(_inputSameSlot($0)), _crossSlotPredecessorCase)
)

// $0 chain constraint index
func chainPredecessorInputIndex : byte(unwrapBytecodeArg(selfSiblingConstraint($0), #chain, 0),32)

func zeroTickOnBranchOnly : or(
	not(isZero(txTimeTick)),
	isBranchTransaction
)

// $0 is chain constraint sibling index. 0xff value means it is genesis output. 
// $1 is total produced amount by transaction, 8 bytes big-endian
func sequencer: and(
	mustSize($0,1),
    mustMinimumAmountOnSequencer, // enforcing minimum amount on sequencer
    if(
        selfIsConsumedOutput,
		require( validInflation($1, txInflationAmount), !!!wrong_inflation_amount ),
		and(
			// produced
			require(not(equal(selfOutputIndex, 0xff)), !!!sequencer_output_can't_be_at_index_0xff),
			require(equal(selfOutputIndex, txSequencerOutputIndex), !!!inconsistent_sequencer_output_index_on_transaction),
			require(not(equal($0, 0xff)), !!!chain_constraint_index_0xff_is_not_alowed),
            require(zeroTickOnBranchOnly, !!!non-branch_sequencer_transaction_cant_be_on_slot_boundary), 
            require(equal($1, txTotalProducedAmount), !!!wrong_total_amount_on_sequencer_output),
                // check chain's past'
			_sequencer( chainPredecessorInputIndex($0) )
		)
    )
)
`

const (
	SequencerConstraintName     = "sequencer"
	sequencerConstraintTemplate = SequencerConstraintName + "(%d, u64/%d)"
)

type SequencerConstraint struct {
	// must point to the sibling chain constraint
	ChainConstraintIndex byte
	// must be equal to the total produced amount of the transaction
	TotalProducedAmount uint64
}

func NewSequencerConstraint(chainConstraintIndex byte, totalProducedAmount uint64) *SequencerConstraint {
	return &SequencerConstraint{
		ChainConstraintIndex: chainConstraintIndex,
		TotalProducedAmount:  totalProducedAmount,
	}
}

func MaxInflationFromPredecessorAmount(amount uint64) uint64 {
	return amount / MaxInflationPerBranchFraction
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.source())
}

func (s *SequencerConstraint) String() string {
	return fmt.Sprintf("%s(%d, %s)", SequencerConstraintName, s.ChainConstraintIndex, util.GoTh(s.TotalProducedAmount))
}

func (s *SequencerConstraint) source() string {
	return fmt.Sprintf(sequencerConstraintTemplate, s.ChainConstraintIndex, s.TotalProducedAmount)
}

func SequencerConstraintFromBytes(data []byte) (*SequencerConstraint, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 2)
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

	totalBin := easyfl.StripDataPrefix(args[1])
	if len(totalBin) != 8 {
		return nil, fmt.Errorf("wrong totalProducedAmount parameter")
	}

	return &SequencerConstraint{
		ChainConstraintIndex: cci,
		TotalProducedAmount:  binary.BigEndian.Uint64(totalBin),
	}, nil
}

func initSequencerConstraint() {
	easyfl.Extend("mustMinimumAmountOnSequencer", minimumAmountOnSeqSource)
	easyfl.Extend("validInflation", validInflationSource)
	easyfl.MustExtendMany(sequencerConstraintSource)

	example := NewSequencerConstraint(4, 1337)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 2)
	util.AssertNoError(err)
	util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")

	cciBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(cciBin) == 1, "len(cciBin) == 1")
	util.Assertf(cciBin[0] == 4, "cciBin[0] == 4")

	totalBin := easyfl.StripDataPrefix(args[1])
	util.Assertf(len(totalBin) == 8, "len(totalBin) == 8")
	util.Assertf(binary.BigEndian.Uint64(totalBin) == 1337, "binary.BigEndian.Uint64(totalBin) == 1337")

	registerConstraint(SequencerConstraintName, prefix, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytes(data)
	})
}
