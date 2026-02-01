package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl/tuples"
)

/*
The following defines Proxima transaction model, library of constraints and other functions
in addition to the base library provided by EasyFL

All integers are treated big-endian. This way lexicographical order coincides with the arithmetic order.

The validation context is a tree-like data structure which is validated by evaluating all constraints in it
consumed and produced outputs. The rest of the validation should be done by the logic outside the data itself.
The tree-like data structure is a tuples.Array, treated as a tree.

Constants which define validation context data tree branches. Structure of the data tree:

(root)
  -- TransactionTuple = 0x00
       -- TxInputIDs = 0x00     (path 0x0000)  -- contains up to 256 inputs, the IDs of consumed outputs
       -- TxUnlockData = 0x01 (path 0x0001)  -- contains unlock params for each input
       -- TxOutputs     = 0x02       (path 0x0002)  -- contains up to 256 produced outputs
       -- TxSignatureData = 0x03    (path 0x0003)  -- contains mandatory signature data. 0 byte signature type,
                                    the rest is proper signature of the transaction essence and the public key, depending on the type
       -- TxSequencerDataBytes = 04  (path 0x0004)
       -- TxTimestamp = 0x05          (path 0x0005)  -- mandatory timestamp of the transaction
       -- TxInputCommitment = 0x06    (path 0x0006)  -- blake2b hash of the all consumed outputs (which are under path 0x1000)
       -- TxEndorsements = 0x07       (path 0x0007)  -- list of transaction IDs of endorsed transaction
       -- TxExplicitBaseline = 0x08       (path 0x0008)  -- list of transaction IDs of endorsed transaction
       -- TxOtherData = 0x09     (path 0x0009)  -- list of local libraries in its binary form
  -- ConsumedTuple = 0x01
       -- ConsumedOutputsBranch = 0x00 (path 0x0100) -- all consumed outputs, up to 256

All consumed outputs ar contained in the tree element under path 0x0100
A input id is at path 0x0001ii, where (ii) is 1-byte index of the consumed input in the transaction
This way:
	- the corresponding consumed output is located at path 0x0100ii (replacing 2 byte path prefix with 0x0100)
	- the corresponding unlock-parameters is located at path 0x0000ii (replacing 2 byte path prefix with 0x0000)
*/

// Top level tuple indices
const (
	// TransactionTuple is nested tuples representing the transaction
	TransactionTuple = byte(iota)
	// ConsumedTuple is sub-tuple of consumed UTXOs
	ConsumedTuple
)

// Transaction subtree
const (
	TxConstraints = byte(iota)
	TxTimestamp
	TxSequencerDataBytes
	TxSignatureData
	TxInputCommitment
	TxExplicitBaseline
	TxInputIDs
	TxUnlockData
	TxOutputs
	TxEndorsements
	TxOtherData
	TxTreeTupleNumElements
)

const ConsumedOutputsBranch = byte(0)

var (
	PathToConsumedOutputs               = tuples.Path(ConsumedTuple, ConsumedOutputsBranch)
	PathToTxConstraints                 = tuples.Path(TransactionTuple, TxConstraints)
	PathToProducedOutputs               = tuples.Path(TransactionTuple, TxOutputs)
	PathToUnlockParams                  = tuples.Path(TransactionTuple, TxUnlockData)
	PathToInputIDs                      = tuples.Path(TransactionTuple, TxInputIDs)
	PathToEndorsements                  = tuples.Path(TransactionTuple, TxEndorsements)
	PathToSequencerAndStemOutputIndices = tuples.Path(TransactionTuple, TxSequencerDataBytes)
	PathToTimestamp                     = tuples.Path(TransactionTuple, TxTimestamp)
	PathToSignature                     = tuples.Path(TransactionTuple, TxSignatureData)
	PathToInputCommitment               = tuples.Path(TransactionTuple, TxInputCommitment)
	PathToExplicitBaseline              = tuples.Path(TransactionTuple, TxExplicitBaseline)
	PathToOtherData                     = tuples.Path(TransactionTuple, TxOtherData)
)

// Mandatory output block indices
const (
	ConstraintIndexAmounts = byte(iota)
	ConstraintIndexLock
	ConstraintIndexFirstOptionalConstraint
)

func pathConstantsUpgrade0() string {
	return fmt.Sprintf(_pathConstantsYAML,
		TransactionTuple,
		PathToTxConstraints.Hex(),
		PathToConsumedOutputs.Hex(),
		PathToProducedOutputs.Hex(),
		PathToUnlockParams.Hex(),
		PathToInputIDs.Hex(),
		PathToSignature.Hex(),
		PathToSequencerAndStemOutputIndices.Hex(),
		PathToInputCommitment.Hex(),
		PathToEndorsements.Hex(),
		PathToExplicitBaseline.Hex(),
		PathToTimestamp.Hex(),
		PathToOtherData.Hex(),
		ConstraintIndexAmounts,
		ConstraintIndexLock,
	)
}

const _pathConstantsYAML = `
functions:
   -
      sym: pathToTransaction
      numArgs: 0
      source: %d
   -
      sym: pathToTxConstraints
      numArgs: 0
      source: 0x%s
   -
      sym: pathToConsumedOutputs
      numArgs: 0
      source: 0x%s
   -
      sym: pathToProducedOutputs
      numArgs: 0
      source: 0x%s
   -
      sym: pathToUnlockParams
      numArgs: 0
      source: 0x%s
   -
      sym: pathToInputIDs
      numArgs: 0
      source: 0x%s
   -
      sym: pathToSignatureData
      numArgs: 0
      source: 0x%s
   -
      sym: pathToSequencerDataBytes
      numArgs: 0
      source: 0x%s
   -
      sym: pathToInputCommitment
      numArgs: 0
      source: 0x%s
   -
      sym: pathToEndorsements
      numArgs: 0
      source: 0x%s
   -
      sym: pathToExplicitBaseline
      numArgs: 0
      source: 0x%s
   -
      sym: pathToTimestamp
      numArgs: 0
      source: 0x%s
   -
      sym: pathToOtherData
      numArgs: 0
      source: 0x%s
   -
      sym: amountsConstraintIndex
      numArgs: 0
      source: %d
   -
      sym: lockConstraintIndex
      numArgs: 0
      source: %d
`
