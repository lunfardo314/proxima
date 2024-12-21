package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

// ChainConstraint is a chain constraint
type ChainConstraint struct {
	// ID all-0 for origin
	ID ChainID
	// 0xFF for origin, 0x00 for state transition, other reserved
	TransitionMode byte
	// Previous index of the consumed chain input with the same ID. Must be 0xFF for the origin
	PredecessorInputIndex      byte
	PredecessorConstraintIndex byte
}

const (
	ChainConstraintName     = "chain"
	chainConstraintTemplate = ChainConstraintName + "(0x%s)"
)

func NewChainConstraint(id ChainID, prevOut, prevBlock, mode byte) *ChainConstraint {
	return &ChainConstraint{
		ID:                         id,
		TransitionMode:             mode,
		PredecessorInputIndex:      prevOut,
		PredecessorConstraintIndex: prevBlock,
	}
}

func NewChainOrigin() *ChainConstraint {
	return NewChainConstraint(NilChainID, 0xff, 0xff, 0xff)
}

func (ch *ChainConstraint) IsOrigin() bool {
	if ch.ID != NilChainID {
		return false
	}
	if ch.PredecessorInputIndex != 0xff {
		return false
	}
	if ch.PredecessorConstraintIndex != 0xff {
		return false
	}
	if ch.TransitionMode != 0xff {
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
	return ch.Source()
}

func (ch *ChainConstraint) Source() string {
	return fmt.Sprintf(chainConstraintTemplate,
		hex.EncodeToString(common.Concat(ch.ID[:], ch.PredecessorInputIndex, ch.PredecessorConstraintIndex, ch.TransitionMode)))
}

func ChainConstraintFromBytes(data []byte) (*ChainConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != ChainConstraintName {
		return nil, fmt.Errorf("ChainConstraintFromBytes: not a chain constraint")
	}
	constraintData := easyfl.StripDataPrefix(args[0])
	if len(constraintData) != ChainIDLength+3 {
		return nil, fmt.Errorf("ChainConstraintFromBytes: wrong data len")
	}
	ret := &ChainConstraint{
		PredecessorInputIndex:      constraintData[ChainIDLength],
		PredecessorConstraintIndex: constraintData[ChainIDLength+1],
		TransitionMode:             constraintData[ChainIDLength+2],
	}
	copy(ret.ID[:], constraintData[:ChainIDLength])
	return ret, nil
}

// NewChainUnlockParams unlock parameters for the chain constraint. 3 bytes:
// 0 - successor output index
// 1 - successor block index
// 2 - transition mode must be equal to the transition mode in the successor constraint data
func NewChainUnlockParams(successorOutputIdx, successorConstraintBlockIndex, transitionMode byte) []byte {
	return []byte{successorOutputIdx, successorConstraintBlockIndex, transitionMode}
}

func addChainConstraint(lib *Library) {
	lib.extendWithConstraint(ChainConstraintName, chainConstraintSource, 1, func(data []byte) (Constraint, error) {
		return ChainConstraintFromBytes(data)
	}, initTestChainConstraint)
}

func initTestChainConstraint() {
	example := NewChainOrigin()
	back, err := ChainConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(back.Bytes(), example.Bytes()), "inconsistency in "+ChainConstraintName)

	_, err = L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	chainConstraintInlineTest()
}

// inline test
func chainConstraintInlineTest() {
	var chainID ChainID
	chainID = blake2b.Sum256([]byte("dummy"))
	{
		chainIDBack, err := ChainIDFromBytes(chainID.Bytes())
		util.AssertNoError(err)
		util.Assertf(chainIDBack == chainID, "chainIDBack == chainID")
	}
	{
		chainConstr := NewChainConstraint(chainID, 0, 0, 0xff)
		chainConstrBack, err := ChainConstraintFromBytes(chainConstr.Bytes())
		util.AssertNoError(err)
		util.Assertf(*chainConstrBack == *chainConstr, "*chainConstrBack == *chainConstr")
	}
}

const chainConstraintSource = `
// chain(<chain constraint data>)
// <chain constraint data: 35 bytes:
// - 0-31 bytes chain id 
// - 32 byte predecessor input index 
// - 33 byte predecessor block index 
// - 34 byte transition mode 

// reserved value of the chain constraint data at origin
func originChainData: concat(repeat(0,32), 0xffffff)
func destroyUnlockParams : 0xffffff

// parsing chain constraint data
// $0 - chain constraint data
func chainID : slice($0, 0, 31)

func requireChainTransition : require(
    and(
       not(equal(chainID($0), repeat(0,32))),
       isZero(byte($0, 34))
	),
    !!!must_be_a_chain_transition
)

func transitionMode: byte($0, 34)
func predecessorConstraintIndex : slice($0, 32, 33) // 2 bytes

// accessing predecessor data
func chainPredecessorInputID : inputIDByIndex(byte($0,32))

// unlock parameters for the chain constraint. 3 bytes: 
// 0 - successor output index 
// 1 - successor block index
// 2 - transition mode must be equal to the transition mode in the successor constraint data 

// only called for produced output
// $0 - self produced constraint data
// $1 - predecessor data
func validPredecessorData : and(
	if(
		isZero(chainID($1)), 
		and(
			// case 1: predecessor is origin. ChainID must be blake2b hash of the corresponding input ID 
			equal($1, originChainData),
			equal(chainID($0), blake2b(chainPredecessorInputID($0)))
		),
		and(
			// case 2: normal transition
			equal(chainID($0), chainID($1)),
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
	evalArgumentBytecode(
		consumedConstraintByIndex($0),
		selfBytecodePrefix,
		0
	)

// $0 - self chain data (consumed)
// $1 - successor constraint parsed data (produced)
func validSuccessorData : and(
		if (
			// if chainID = 0, it must be origin data
			// otherwise chain IDs must be equal on both sides
			isZero(chainID($0)),
			equal($0, originChainData),
			equal(chainID($0),chainID($1))
		),
		// the successor (produced) must point to the consumed (self)
		equal(predecessorConstraintIndex($1), selfConstraintIndex)
)

// chain successor data is computed form in the context of the consumed output
// from the selfUnlock data
func chainSuccessorData : 
	evalArgumentBytecode(
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
         and(
            isZero(chainID($0)),
            selfIsProducedOutput
         ),
         or(
            // enforcing valid constraint data of the origin: concat(repeat(0,32), 0xffffff)
            equal($0, originChainData), 
            !!!chain_wrong_origin
         ),
         nil
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
            validSuccessorData($0, chainSuccessorData),     
            !!!chain_wrong_successor
          )	
       ), 
       and(
          // 'produced' side case, checking if predecessor is valid
           selfIsProducedOutput,
           or(
              // 'produced' side case checking if predecessor is valid
              validPredecessorData($0, chainPredecessorData( predecessorConstraintIndex($0) )),
              !!!chain_wrong_predecessor
           )
       ),
       !!!chain_constraint_failed
   )
)

// $0 - chain constraint index in the produced output
func chainPredecessorInputIndex : byte(evalArgumentBytecode(selfSiblingConstraint($0), #chain, 0),32)
`
