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
       -- TxVersion = 0x00           (path 0x0000)  -- uint16 big-endian, library upgrade index
       -- TxTimestamp = 0x01         (path 0x0001)  -- mandatory 5-byte ledger timestamp
       -- TxSequencerDataBytes = 0x02 (path 0x0002) -- sequencer milestone data
       -- TxSignatureData = 0x03     (path 0x0003)  -- mandatory signature data. 0 byte signature type,
                                     the rest is proper signature of the transaction essence and the public key, depending on the type
       -- TxInputCommitment = 0x04   (path 0x0004)  -- blake2b hash of all consumed outputs
       -- TxExplicitBaseline = 0x05  (path 0x0005)  -- optional explicit baseline transaction ID
       -- TxInputIDs = 0x06         (path 0x0006)  -- contains up to 256 inputs, the IDs of consumed outputs
       -- TxUnlockData = 0x07       (path 0x0007)  -- contains unlock params for each input
       -- TxOutputs = 0x08          (path 0x0008)  -- contains up to 256 produced outputs
       -- TxEndorsements = 0x09     (path 0x0009)  -- list of transaction IDs of endorsed transactions
       -- TxConstraints = 0x0a      (path 0x000a)  -- reserved for transaction-level constraints
       -- TxOtherData = 0x0b        (path 0x000b)  -- list of local libraries in binary form
  -- ConsumedTuple = 0x01
       -- ConsumedOutputsBranch = 0x00 (path 0x0100) -- all consumed outputs, up to 256

All consumed outputs are contained in the tree element under path 0x0100
An input id is at path 0x0006ii, where (ii) is 1-byte index of the consumed input in the transaction
This way:
	- the corresponding consumed output is located at path 0x0100ii (replacing 2 byte path prefix with 0x0100)
	- the corresponding unlock-parameters is located at path 0x0007ii (replacing 2 byte path prefix with 0x0007)
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
	TxVersion = byte(iota) // uint16 big-endian, 2 bytes: library upgrade index
	TxTimestamp
	TxSequencerDataBytes
	TxSignatureData
	TxInputCommitment
	TxExplicitBaseline
	TxInputIDs
	TxUnlockData
	TxOutputs
	TxEndorsements
	TxConstraints
	TxOtherData
	TxTreeTupleNumElements
)

const ConsumedOutputsBranch = byte(0)

var (
	PathToRawTransaction     = tuples.Path(TransactionTuple)
	PathToConsumedOutputs    = tuples.Path(ConsumedTuple, ConsumedOutputsBranch)
	PathToTxVersion          = tuples.Path(TransactionTuple, TxVersion)
	PathToTxConstraints      = tuples.Path(TransactionTuple, TxConstraints)
	PathToProducedOutputs    = tuples.Path(TransactionTuple, TxOutputs)
	PathToUnlockParams       = tuples.Path(TransactionTuple, TxUnlockData)
	PathToInputIDs           = tuples.Path(TransactionTuple, TxInputIDs)
	PathToEndorsements       = tuples.Path(TransactionTuple, TxEndorsements)
	PathToSequencerDataBytes = tuples.Path(TransactionTuple, TxSequencerDataBytes)
	PathToTimestamp          = tuples.Path(TransactionTuple, TxTimestamp)
	PathToSignature          = tuples.Path(TransactionTuple, TxSignatureData)
	PathToInputCommitment    = tuples.Path(TransactionTuple, TxInputCommitment)
	PathToExplicitBaseline   = tuples.Path(TransactionTuple, TxExplicitBaseline)
	PathToOtherData          = tuples.Path(TransactionTuple, TxOtherData)
)

// Mandatory output block indices.
//
// Layout: [0] amounts, [1] index-value tuple, [2] lock, [3] chain (when present).
// See claude/utxo-indexing.md §4.
const (
	ConstraintIndexAmounts     = byte(iota) // 0
	ConstraintIndexIndexValues              // 1: tuple of indexable values for this UTXO (Phase A: empty placeholder)
	ConstraintIndexLock                     // 2
	ConstraintIndexChain                    // 3 (when present)
)

func pathConstantsUpgrade0() string {
	return fmt.Sprintf(_pathConstantsYAML,
		TransactionTuple,
		PathToTxVersion.Hex(),
		PathToTxConstraints.Hex(),
		PathToConsumedOutputs.Hex(),
		PathToProducedOutputs.Hex(),
		PathToUnlockParams.Hex(),
		PathToInputIDs.Hex(),
		PathToSignature.Hex(),
		PathToSequencerDataBytes.Hex(),
		PathToInputCommitment.Hex(),
		PathToEndorsements.Hex(),
		PathToExplicitBaseline.Hex(),
		PathToTimestamp.Hex(),
		PathToOtherData.Hex(),
		ConstraintIndexAmounts,
		ConstraintIndexIndexValues,
		ConstraintIndexLock,
		ConstraintIndexChain,
	)
}

const _pathConstantsYAML = `
functions:
   -
      sym: pathToTransaction
      numArgs: 0
      source: %d
   -
      sym: pathToTxVersion
      numArgs: 0
      source: 0x%s
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
      sym: indexValuesConstraintIndex
      numArgs: 0
      source: %d
   -
      sym: lockConstraintIndex
      numArgs: 0
      source: %d
   -
      sym: chainConstraintIndex
      numArgs: 0
      source: %d
`
