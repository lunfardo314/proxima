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

// TODO add start slot and start amount

// ChainConstraint is a chain constraint
type ChainConstraint struct {
	// ID all-0 for origin
	ID base.ChainID
	// Predecessor output index with the same ID. Must be 0xFF for the origin
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
	chainConstraintTemplate = ChainConstraintName + "(0x%s, z1/%d, z1/%d, z32/%d, z64/%d)"
)

func NewChainConstraint(id base.ChainID, predOutputIndex, predConstraintIndex byte, originSlot base.Slot, originAmount uint64) *ChainConstraint {
	return &ChainConstraint{
		ID:                         id,
		PredecessorInputIndex:      predOutputIndex,
		PredecessorConstraintIndex: predConstraintIndex,
		OriginSlot:                 originSlot,
		OriginAmount:               originAmount,
	}
}

func NewChainOrigin(startSlot base.Slot, startAmount uint64) *ChainConstraint {
	return NewChainConstraint(base.NilChainID, 0xff, 0xff, startSlot, startAmount)
}

func (ch *ChainConstraint) IsOrigin() bool {
	if ch.ID != base.NilChainID {
		return false
	}
	if ch.PredecessorInputIndex != 0xff {
		return false
	}
	if ch.PredecessorConstraintIndex != 0xff {
		return false
	}
	return true
}

func (ch *ChainConstraint) Name() string {
	return ChainConstraintName
}

func (ch *ChainConstraint) Bytes() []byte {
	return mustBinFromSource(ch.Source())
}

func (ch *ChainConstraint) String() string {
	if ch.IsOrigin() {
		return fmt.Sprintf("%s(ORIGIN)", ChainConstraintName)
	}
	return fmt.Sprintf("%s(%s, predOutIdx=%d, predConstrIdx=%d, originSlot=%d, originAmount=%s)",
		ChainConstraintName, ch.ID.String(), ch.PredecessorInputIndex, ch.PredecessorConstraintIndex,
		ch.OriginSlot, util.Th(ch.OriginAmount))
}

func (ch *ChainConstraint) Source() string {
	return fmt.Sprintf(chainConstraintTemplate,
		hex.EncodeToString(ch.ID[:]), ch.PredecessorInputIndex, ch.PredecessorConstraintIndex,
		ch.OriginSlot, ch.OriginAmount)
}

func ChainConstraintFromBytes(data []byte) (*ChainConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 5)
	if err != nil {
		return nil, err
	}
	if sym != ChainConstraintName {
		return nil, fmt.Errorf("ChainConstraintFromBytes: not a chain constraint")
	}

	ret := &ChainConstraint{}
	if ret.ID, err = base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, err
	}
	if ret.PredecessorInputIndex, err = easyfl_util.ByteFromBytes(easyfl.StripDataPrefix(args[1])); err != nil {
		return nil, err
	}
	if ret.PredecessorConstraintIndex, err = easyfl_util.ByteFromBytes(easyfl.StripDataPrefix(args[2])); err != nil {
		return nil, err
	}
	sl, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, err
	}
	ret.OriginSlot = base.Slot(sl)
	if ret.OriginAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4])); err != nil {
		return nil, err
	}
	return ret, nil
}

// NewChainUnlockParams unlock parameters for the chain constraint. 3 bytes:
// 0 - successor output index
// 1 - successor block index
// 2 - transition mode must be equal to the transition mode in the successor constraint data
func NewChainUnlockParams(successorOutputIdx, successorConstraintIndex, transitionMode byte) []byte {
	return []byte{successorOutputIdx, successorConstraintIndex, transitionMode}
}

var FinishChainUnlockParams = []byte{0xff, 0xff, 0xff}

func registerChainConstraint(lib *Library) {
	lib.mustRegisterConstraint(ChainConstraintName, 5, func(data []byte) (Constraint, error) {
		return ChainConstraintFromBytes(data)
	}, initTestChainConstraintInlineTest)
}

func initTestChainConstraintInlineTest() {
	example := NewChainOrigin(1000, 10_000_000)
	back, err := ChainConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(back.Bytes(), example.Bytes()), "inconsistency in "+ChainConstraintName)
	//util.Assertf(back == example, "inconsistency: back==example")
	util.Assertf(back.OriginSlot == 1000, "back.StartSlot == 1000")
	util.Assertf(back.OriginAmount == 10_000_000, "back.StartAmount == 10_000_000")

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

// TODO rewrite chain constraint

const chainConstraintSourceOld = `
// chain(<chain constraint data>)
// <chain constraint data: 35 bytes:
// - 0-31 bytes chain id 
// - 32 byte predecessor input index 
// - 33 byte predecessor block index 
// - 34 byte transition mode 

// check $0 reserved value of the chain constraint data at origin
func isOriginChainData: equal($0, 0x0000000000000000000000000000000000000000000000000000000000000000ffffff)
func destroyUnlockParams : 0xffffff

// parsing chain constraint data
// $0 - chain constraint data
func chainID : slice($0, 0, 31)

// $0 - chain constraint data
func transitionMode: byte($0, 34)
func predecessorConstraintIndex : slice($0, 32, 33) // 2 bytes

// unlock parameters for the chain constraint. 3 bytes: 
// 0 - successor output index 
// 1 - successor constraint block index
// 2 - transition mode must be equal to the transition mode in the successor constraint data 

// only called for produced output
// $0 - self produced constraint data
// $1 - predecessor data
func _validPredecessorData : and(
	if(
		isZero(chainID($1)), 
		and(
			// case 1: predecessor is origin. ChainID must be blake2b hash of the corresponding input id 
			isOriginChainData($1),
			equal(chainID($0), blake2b(inputIDByIndex(byte($0,32))))
		),
		and(
			// case 2: normal transition
			equal(chainID($0), chainID($1)),
            equal(),
		)
	),
	equal(
		// enforcing equal transition mode on unlock data and on the produced output
		transitionMode($0),
		byte(unlockParamsByConstraintIndex(predecessorConstraintIndex($0)),2)
	)
)

// $0 - predecessor constraint index
func chainPredecessorData:
	parseInlineDataArgument(
		consumedConstraintByIndex($0),
		selfBytecodePrefix,
		0
	)

// $0 - self chain data (consumed)
// $1 - successor constraint parsed data (produced)
func _validSuccessorData : and(
		if (
			// if chainID = 0, it must be origin data
			// otherwise chain IDs must be equal on both sides
			isZero(chainID($0)),
			isOriginChainData($0),
			equal(chainID($0),chainID($1))
		),
		// the successor (produced) must point to the consumed (self)
		equal(predecessorConstraintIndex($1), selfConstraintIndex)
)

// $0 - chain data
// $1 - origin slot
// $2 - origin amount
func _validOriginData : concat($2, 1)


// chain successor data is computed in the context of the consumed output
// from the selfUnlock data
func chainSuccessorData : 
	parseInlineDataArgument(
		producedConstraintByIndex(slice(selfUnlockParameters,0,1)),
		selfBytecodePrefix,
		0
	)

// Constraint Source: chain($0)
// $0 - 35-bytes data: 
//     32 bytes chain id
//     1 byte predecessor input index 
//     1 byte predecessor constraint index
//     1 byte transition mode
// Transition mode: 
//     0x00 - state transition
//     0xff - origin state, can be any other values. 
// $1 - origin slot
// $2 - origin amount
// -----
// unlock parameters for the chain constraint. 3 bytes:
// 0 - successor output index
// 1 - successor block index
// 2 - transition mode must be equal to the transition mode in the successor constraint data
func chain: and(
      // chain constraint cannot be on output with index 0xff = 255
   not(equal(selfOutputIndex, 0xff)),  
   or(
      if(
        // if it is produced output with zero-chainID, it is chain origin.
         and( selfIsProducedOutput, isZero(chainID($0))),
         require(
             and(
                isOriginChainData($0),
                equalUint($1, txSlot),
                equalUint(selfAmountValue, $2)
             ),
             !!!wrong_chain_origin_data
         ),
         0x
       ),
        // check validity of chain transition. Unlock data of the constraint 
        // must point to the valid successor (in case of consumed output) 
        // or predecessor (in case of produced output) 
       and(
           // 'consumed' side case, checking if unlock params and successor is valid
          selfIsConsumedOutput,
          or(
               // consumed chain output is being destroyed (no successor)
            equal(selfUnlockParameters, destroyUnlockParams),
               // or it must be unlocked by pointing to the successor
            _validSuccessorData($0, chainSuccessorData),     
            !!!chain_wrong_successor
          )	
       ), 
       and(
          // 'produced' side case, checking if predecessor is valid
           selfIsProducedOutput,
           require(_validPredecessorData($0, chainPredecessorData( predecessorConstraintIndex($0) )), !!!chain_wrong_predecessor_chain_data),
           require(_validOriginData($0, $1, $2), !!!invalid_chain_origin_data)
       ),
       !!!chain_constraint_failed
   )
)

// $0 - chain constraint index
func selfChainData : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 0)

// $0 - chain constraint index in the produced output
// Returns chain predecessor input by 1-byte index
func selfChainPredecessorInputIndex : byte(selfChainData($0),32)

// $0 - chain constraint index in the produced output
// Returns chain predecessor timestamp in the context of produced successor by by 1-byte index of the chain constraint 
func selfChainPredecessorTimestamp : timestampOfInputByIndex(selfChainPredecessorInputIndex($0))

`

const chainConstraintSource = `
func isChainOriginID: equal($0, 0x0000000000000000000000000000000000000000000000000000000000000000)
func destroyUnlockParams : 0xffffff

// $0 - predecessor output index
// $1 - predecessor constraint index
func chainPredecessorChainID:
	parseInlineDataArgument(
		consumedConstraintByIndex($0,$1),
		selfBytecodePrefix,
		0
	)
// $0 - predecessor output index
// $1 - predecessor constraint index
func chainPredecessorOriginSlot:
	parseInlineDataArgument(
		consumedConstraintByIndex($0,$1),
		selfBytecodePrefix,
		3
	)
// $0 - predecessor output index
// $1 - predecessor constraint index
func chainPredecessorOriginAmount:
	parseInlineDataArgument(
		consumedConstraintByIndex($0,$1),
		selfBytecodePrefix,
		4
	)

// $0 - chain ID
// $1 - predecessor output index
// $2 - predecessor constraint index
// $3 - origin slot
// $4 - origin amount
func _validChainProduced : 
if(
   isChainOriginID($0),
   require(
     and(equal($1, 0xff), equal($2, 0xff), equalUint($3, txSlot), equalUint($4, selfAmountValue)),
     !!!invalid_chain_origin_data
   ),
   and(
	   if(
		 isChainOriginID(chainPredecessorChainID($1,$2)),
		 require( equal( blake2b( inputIDByIndex($1) ), $0),  !!!wrong_predecessor_chain_ID_1),
		 require( equal( chainPredecessorChainID($1,$2), $0), !!!wrong_predecessor_chain_ID_2) 
	   ),
       require(equalUint($3, chainPredecessorOriginSlot($1,$2)), !!!invalid_origin_slot),
       require(equalUint($4, chainPredecessorOriginAmount($1,$2)), !!!invalid_origin_amount),
   )
)

    
// $0 - chain ID
// $1 - predecessor output index
// $2 - predecessor constraint index
// $3 - origin slot
// $4 - origin amount
func _validChainConsumed : concat($4, 1)

// $0 - chain ID
// $1 - predecessor output index
// $2 - predecessor constraint index
// $3 - origin slot
// $4 - origin amount
func chain : and(
      // chain constraint cannot be on output with index 0xff = 255
   not(equal(selfOutputIndex, 0xff)),  
   or(
      and(
         selfIsProducedOutput,
         _validChainProduced($0,$1,$2,$3,$4)
      ),
      and(
         selfIsConsumedOutput,
         _validChainConsumed($0,$1,$2,$3,$4)
      ),

   )
)

// $0 - chain constraint index
func selfChainID : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 0)
func selfChainPredInputIndex : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 1)
func selfChainPredConstraintIndex : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 2)
func selfOriginSlot : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 3)
func selfOriginAmount : parseInlineDataArgument(selfSiblingConstraint($0), #chain, 4)

TODO
func selfChainPredecessorTimestamp : timestampOfInputByIndex(selfChainPredecessorInputIndex($0))

`
