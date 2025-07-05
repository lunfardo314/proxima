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
  -- TransactionBranch = 0x00
       -- TxUnlockData = 0x00 (path 0x0000)  -- contains unlock params for each input
       -- TxInputIDs = 0x01     (path 0x0001)  -- contains up to 256 inputs, the IDs of consumed outputs
       -- TxOutputBranch = 0x02       (path 0x0002)  -- contains up to 256 produced outputs
       -- TxSignature = 0x03          (path 0x0003)  -- contains the only signature of the essence. It is mandatory
       -- TxTimestamp = 0x04          (path 0x0004)  -- mandatory timestamp of the transaction
       -- TxInputCommitment = 0x05    (path 0x0005)  -- blake2b hash of the all consumed outputs (which are under path 0x1000)
       -- TxEndorsements = 0x06       (path 0x0006)  -- list of transaction IDs of endorsed transaction
       -- TxLocalLibraries = 0x07     (path 0x0007)  -- list of local libraries in its binary form
  -- ConsumedBranch = 0x01
       -- ConsumedOutputsBranch = 0x00 (path 0x0100) -- all consumed outputs, up to 256

All consumed outputs ar contained in the tree element under path 0x0100
A input id is at path 0x0001ii, where (ii) is 1-byte index of the consumed input in the transaction
This way:
	- the corresponding consumed output is located at path 0x0100ii (replacing 2 byte path prefix with 0x0100)
	- the corresponding unlock-parameters is located at path 0x0000ii (replacing 2 byte path prefix with 0x0000)
*/

// Top level branches
const (
	TransactionBranch = byte(iota)
	ConsumedBranch
)

// Transaction tree
const (
	TxInputIDs = byte(iota)
	TxUnlockData
	TxOutputs
	TxSignature
	TxSequencerAndStemOutputIndices
	TxTimestamp
	TxTotalProducedAmount
	TxInputCommitment
	TxEndorsements
	TxExplicitBaseline
	TxLocalLibraries
	TxTreeIndexMax
)

const ConsumedOutputsBranch = byte(0)

var (
	PathToConsumedOutputs               = tuples.Path(ConsumedBranch, ConsumedOutputsBranch)
	PathToProducedOutputs               = tuples.Path(TransactionBranch, TxOutputs)
	PathToUnlockParams                  = tuples.Path(TransactionBranch, TxUnlockData)
	PathToInputIDs                      = tuples.Path(TransactionBranch, TxInputIDs)
	PathToSignature                     = tuples.Path(TransactionBranch, TxSignature)
	PathToSequencerAndStemOutputIndices = tuples.Path(TransactionBranch, TxSequencerAndStemOutputIndices)
	PathToInputCommitment               = tuples.Path(TransactionBranch, TxInputCommitment)
	PathToEndorsements                  = tuples.Path(TransactionBranch, TxEndorsements)
	PathToExplicitBaseline              = tuples.Path(TransactionBranch, TxExplicitBaseline)
	PathToLocalLibraries                = tuples.Path(TransactionBranch, TxLocalLibraries)
	PathToTimestamp                     = tuples.Path(TransactionBranch, TxTimestamp)
	PathToTotalProducedAmount           = tuples.Path(TransactionBranch, TxTotalProducedAmount)
)

// Mandatory output block indices
const (
	ConstraintIndexAmounts = byte(iota)
	ConstraintIndexLock
	ConstraintIndexFirstOptionalConstraint
)

func pathConstants() string {
	return fmt.Sprintf(_pathConstantsYAML,
		TransactionBranch,
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
		PathToTotalProducedAmount.Hex(),
		PathToLocalLibraries.Hex(),
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
      sym: pathToSignature
      numArgs: 0
      source: 0x%s
   -
      sym: pathToSeqAndStemOutputIndices
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
      sym: pathToTotalProducedAmount
      numArgs: 0
      source: 0x%s
   -
      sym: pathToLocalLibraries
      numArgs: 0
      source: 0x%s
   -
      sym: amountConstraintIndex
      numArgs: 0
      source: %d
   -
      sym: lockConstraintIndex
      numArgs: 0
      source: %d
`
