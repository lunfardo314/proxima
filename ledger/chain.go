package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// ChainConstraint is a chain constraint
type ChainConstraint struct {
	// ChainID all-0 for origin
	ChainID base.ChainID
	// Predecessor output index with the same ChainID. Must be 0xFF for the origin
	PredecessorInputIndex byte
	// Predecessor constraint index. Must be 0xff for the origin
	PredecessorConstraintIndex byte
	// slot of the origin chain output
	OriginSlot base.Slot
	// amount on the chain at the origin
	OriginAmount uint64
}

const (
	ChainConstraintName     = "chain"
	chainConstraintTemplate = ChainConstraintName + "(0x%s, 0x%s, z32/%d, z64/%d)"
)

func NewChainConstraint(id base.ChainID, predOutputIndex, predConstraintIndex byte, originSlot base.Slot, originAmount uint64) *ChainConstraint {
	return &ChainConstraint{
		ChainID:                    id,
		PredecessorInputIndex:      predOutputIndex,
		PredecessorConstraintIndex: predConstraintIndex,
		OriginSlot:                 originSlot,
		OriginAmount:               originAmount,
	}
}

func NewChainOrigin(startSlot base.Slot, startAmount uint64) *ChainConstraint {
	return NewChainConstraint(base.NilChainID, 0xff, 0xff, startSlot, startAmount)
}

func (cc *ChainConstraint) IsOrigin() bool {
	if cc.ChainID != base.NilChainID {
		return false
	}
	if cc.PredecessorInputIndex != 0xff {
		return false
	}
	if cc.PredecessorConstraintIndex != 0xff {
		return false
	}
	return true
}

func (cc *ChainConstraint) Name() string {
	return ChainConstraintName
}

func (cc *ChainConstraint) Bytes() []byte {
	return mustBinFromSource(cc.Source())
}

func (cc *ChainConstraint) String() string {
	chID := "ORIGIN"
	if !cc.IsOrigin() {
		chID = cc.ChainID.String()
	}
	predRef := []byte{cc.PredecessorInputIndex, cc.PredecessorConstraintIndex}
	return fmt.Sprintf("%s(%s, predRef=%s, originSlot=%d, originAmount=%s)",
		ChainConstraintName, chID, hex.EncodeToString(predRef), cc.OriginSlot, util.Th(cc.OriginAmount))
}

func (cc *ChainConstraint) Source() string {
	predRef := []byte{cc.PredecessorInputIndex, cc.PredecessorConstraintIndex}
	return fmt.Sprintf(chainConstraintTemplate,
		hex.EncodeToString(cc.ChainID[:]), hex.EncodeToString(predRef), cc.OriginSlot, cc.OriginAmount)
}

func ChainConstraintFromBytes(data []byte) (*ChainConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, err
	}
	if sym != ChainConstraintName {
		return nil, fmt.Errorf("ChainConstraintFromBytes: not a chain constraint")
	}

	ret := &ChainConstraint{}
	if ret.ChainID, err = base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, err
	}
	args1 := easyfl.StripDataPrefix(args[1])
	if len(args1) != 2 {
		return nil, fmt.Errorf("ChainConstraintFromBytes: wrong predecessor reference")
	}
	ret.PredecessorInputIndex = args1[0]
	ret.PredecessorConstraintIndex = args1[1]
	sl, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, err
	}
	ret.OriginSlot = base.Slot(sl)
	if ret.OriginAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3])); err != nil {
		return nil, err
	}
	return ret, nil
}

// NewChainUnlockParams unlock parameters for the chain constraint. 3 bytes:
// 0 - successor output index
// 1 - successor block index
// 2 - transition mode must be equal to the transition mode in the successor constraint data
func NewChainUnlockParams(successorOutputIdx, successorConstraintIndex byte) []byte {
	return []byte{successorOutputIdx, successorConstraintIndex}
}

var FinishChainUnlockParams = []byte{0xff, 0xff}

func registerChainConstraint(lib *Library) {
	lib.mustRegisterConstraint(ChainConstraintName, 4, func(data []byte) (Constraint, error) {
		return ChainConstraintFromBytes(data)
	}, initTestChainConstraintInlineTest)
}

func initTestChainConstraintInlineTest() {
	example := NewChainOrigin(1000, 10_000_000)
	back, err := ChainConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(back.Bytes(), example.Bytes()), "inconsistency in "+ChainConstraintName)
	util.Assertf(back.OriginSlot == 1000, "back.OriginSlot == 1000")
	util.Assertf(back.OriginAmount == 10_000_000, "back.OriginAmount == 10_000_000")

	var chainID base.ChainID
	chainID = blake2b.Sum256([]byte("dummy"))
	{
		chainIDBack, err := base.ChainIDFromBytes(chainID.Bytes())
		util.AssertNoError(err)
		util.Assertf(chainIDBack == chainID, "chainIDBack == chainID")
	}
	{
		chainConstr := NewChainConstraint(chainID, 0, 0, 1000, 10_000_000)
		chainConstrBack, err := ChainConstraintFromBytes(chainConstr.Bytes())
		util.AssertNoError(err)
		util.Assertf(*chainConstrBack == *chainConstr, "*chainConstrBack == *chainConstr")
	}
}

const chainConstraintSource = `
func isChainOriginID: equal($0, 0x0000000000000000000000000000000000000000000000000000000000000000)

// $0 - chain ChainID
// $1 - predecessor output index || predecessor constraint index (2 bytes)
// $2 - origin slot
// $3 - origin amount
func _validChainProduced : 
if(
   isChainOriginID($0),
        // chain origin
   require(
     and(equal($1, 0xffff), equalUint($2, txSlot), equalUint($3, selfTokenBalanceValue)),
     !!!invalid_chain_origin_data
   ),
        // NOT chain origin. Crosscheck reference
   require(
     equal($1, atPath(concat(pathToUnlockParams, $1))),
     !!!predecessor_reference_crosscheck_failed
   )
)

// $0 - param number
func _chainSuccessorParam :
	parseInlineDataArgument(
        atPath(concat(pathToProducedOutputs, selfUnlockParameters)),
		selfBytecodePrefix,
		$0
	)

// $0 - chain ChainID
// $1 - origin slot
// $2 - origin amount
func _validChainConsumed : 
or(
      // discontinue chain. Check nothing
   equal(selfUnlockParameters, 0xffff),
      // chain continues
   and (
      require(equal(len(selfUnlockParameters), u64/2), !!!unlock_parameters_must_be_2_bytes),
        // check chainID match
      require(
         if(
           isChainOriginID($0),
           equal(blake2b(inputIDByIndex(selfOutputIndex)), _chainSuccessorParam(0)),
           equal($0, _chainSuccessorParam(0))
         ),
         !!!chain_ID_mismatch_with_successor
      ),
        // crosscheck successor reference
      require(
         equal(selfUnlockParameters, _chainSuccessorParam(1)),
         !!!successor_reference_crosscheck_failed
      ),
      require(
         equal($1, _chainSuccessorParam(2)),
         !!!origin_slot_is_immutable
      ),
      require(
         equal($2, _chainSuccessorParam(3)),
         !!!origin_amount_is_immutable
      ),
   )
)

//func selfInflationAmount : selfAmountAt(1)

//func _producedVRFProof : 
//     parseInlineDataArgument(
//        producedConstraintByIndex(concat(txStemOutputIndex, lockConstraintIndex)), 
//        #stemLock, 
//        1
//     )

// $0 - chain predecessor input index
//func _calcChainInflationAmountForPredecessor :
//     calcChainInflationAmount(
//	    timestampOfInputByIndex($0), 
//        txTimestampBytes,
//	    tokenBalanceByOutputPath(concat(pathToConsumedOutputs,$0)),
//	 )


// $0 - chain ChainID
// $1 - predecessor (input index || chain constraint index) - 2 bytes 
//func _validInflationAmount : 
//or(
//     // zero inflation is always ok
//   isZero(selfInflationAmount),
//   and(
//      require(not(isChainOriginID($0)), !!!inflation_must_be_0_at_chain_origin),
//
//      if(
//         isBranchTransaction,
//              // branch tx. Enforce inflation is calculated from the VRF proof
//         require(
//            equal( selfInflationAmount, branchInflationBonusFromRandomnessProof(_producedVRFProof) ),
//            !!!invalid_branch_inflation_bonus
//         ),
//                   // not branch tx. Enforce valid chain inflation amount
//         require(
//	        lessOrEqualThan( selfInflationAmount, _calcChainInflationAmountForPredecessor(byte($1,0)) ),
//			!!!invalid_chain_inflation_amount
//		 )
//      )
//   )
//)

// $0 - chain ChainID
// $1 - predecessor (input index || chain constraint index) - 2 bytes 
// $2 - origin slot
// $3 - origin amount
// --- unlock data: 2 bytes: (successor output index || successor chain constraint), 0xffff means discontinue chain
func chain : and(
      // chain constraint cannot be on output with index 0xff = 255
   not(equal(selfOutputIndex, 0xff)),
   require(equal(len($0),u64/32), !!!chainID_must_be_32_bytes_long),
   or(
      and(
         selfIsProducedOutput,
         _validChainProduced($0,$1,$2,$3),
         // _validInflationAmount($0,$1)
      ),
      and(
         selfIsConsumedOutput,
         _validChainConsumed($0,$2,$3)
      )
   )
)

// $0 - chain constraint index
func selfChainID : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 0)
func selfChainPredInputIndex : byte(parseInlineDataArgument(selfSiblingConstraint($0), #chain, 1), 0)

// $0 chain constrain index
func selfChainPredecessorTimestamp : timestampOfInputByIndex( byte(parseInlineDataArgument(selfSiblingConstraint($0),#chain,1),0) )

`
