package core

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
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
		and(isBranchTransaction, sequencerFlagON(inputIDByIndex($0))), 
		not(isZero(numEndorsements))
	), 
	!!!sequencer_tx_has_incorrect_cross_slot_chain_predecessor_or_dont_have_any_endorsements
)

// $0 chain predecessor input index
func _sequencer :
or(
	and( equal($0, 0xff), _noChainPredecessorCase ),
	and( _inputSameSlot($0), _sameSlotPredecessorCase($0)),
	and( not(_inputSameSlot($0)), _crossSlotPredecessorCase($0))
)

// $0 chain constraint index
func chainPredecessorInputIndex : byte(parseBytecodeArg(selfSiblingConstraint($0), #chain, 0),32)

func zeroTickOnBranchOnly : or(
	not(isZero(txTimeTick)),
	isBranchTransaction
)

// $0 is chain constraint sibling index
// 0xff value means it is genesis output. 
// $1 u64 minimum fee accepted by the sequencer
func sequencer: and(
	mustSize($0,1),
	mustSize($1,8),
	or(
		selfIsConsumedOutput,  // constraint on consumed not checked
		and(
			// produced
			require(not(equal(selfOutputIndex, 0xff)), !!!sequencer_output_can't_be_at_index_0xff),
			require(equal(selfOutputIndex, txSequencerOutputIndex), !!!inconsistent_sequencer_output_index_on_transaction),
			require(not(equal($0, 0xff)), !!!chain_constraint_index_0xff_is_not_alowed),
            require(zeroTickOnBranchOnly, !!!non-branch_sequencer_transaction_cant_be_on_slot_boundary), 
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

type (
	SequencerConstraint struct {
		ChainConstraintIndex byte // must point to the sibling chain constraint
		MinimumFee           uint64
	}
)

func NewSequencerConstraint(chainConstraintIndex byte, minFee uint64) *SequencerConstraint {
	return &SequencerConstraint{
		ChainConstraintIndex: chainConstraintIndex,
		MinimumFee:           minFee,
	}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.source())
}

func (s *SequencerConstraint) String() string {
	return s.source()
}

func (s *SequencerConstraint) source() string {
	return fmt.Sprintf(sequencerConstraintTemplate, s.ChainConstraintIndex, s.MinimumFee)
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
	minFeeBin := easyfl.StripDataPrefix(args[1])
	if len(minFeeBin) != 8 {
		return nil, fmt.Errorf("wrong minimumFee parameter")
	}
	minFee := binary.BigEndian.Uint64(minFeeBin)
	return &SequencerConstraint{
		ChainConstraintIndex: cci,
		MinimumFee:           minFee,
	}, nil
}

func initSequencerConstraint() {
	easyfl.MustExtendMany(sequencerConstraintSource)

	example := NewSequencerConstraint(4, 100)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 2)
	util.AssertNoError(err)
	util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")

	cciBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(cciBin) == 1, "len(cciBin) == 1")
	util.Assertf(cciBin[0] == 4, "cciBin[0] == 4")

	minFeeBin := easyfl.StripDataPrefix(args[1])
	util.Assertf(len(minFeeBin) == 8, "len(minFeeBin) == 8")
	util.Assertf(binary.BigEndian.Uint64(minFeeBin) == 100, "binary.BigEndian.Uint64(minFeeBin)==100")
	registerConstraint(SequencerConstraintName, prefix, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytes(data)
	})
}
