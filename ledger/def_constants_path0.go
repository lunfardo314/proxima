package ledger

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger/txcore"
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
  -- ConsumedTuple = 0x01
       -- ConsumedOutputsBranch = 0x00 (path 0x0100) -- all consumed outputs, up to 256

All consumed outputs are contained in the tree element under path 0x0100
An input id is at path 0x0006ii, where (ii) is 1-byte index of the consumed input in the transaction
This way:
	- the corresponding consumed output is located at path 0x0100ii (replacing 2 byte path prefix with 0x0100)
	- the corresponding unlock-parameters is located at path 0x0007ii (replacing 2 byte path prefix with 0x0007)
*/

// Top level tuple indices — re-exported from ledger/txcore.
const (
	// TransactionTuple is the nested tuple representing the transaction.
	TransactionTuple = txcore.TransactionTuple
	// ConsumedTuple is the sub-tuple of consumed UTXOs.
	ConsumedTuple = txcore.ConsumedTuple
)

// Transaction subtree indices — re-exported from ledger/txcore.
// The wire-format definitions live in ledger/txcore/tx_layout.go so
// txcore (and the wasm wallet) can build / parse the tuple without
// importing the full ledger package.
const (
	TxVersion              = txcore.TxVersion
	TxTimestamp            = txcore.TxTimestamp
	TxSequencerDataBytes   = txcore.TxSequencerDataBytes
	TxSignatureData        = txcore.TxSignatureData
	TxInputCommitment      = txcore.TxInputCommitment
	TxExplicitBaseline     = txcore.TxExplicitBaseline
	TxInputIDs             = txcore.TxInputIDs
	TxUnlockData           = txcore.TxUnlockData
	TxOutputs              = txcore.TxOutputs
	TxEndorsements         = txcore.TxEndorsements
	TxConstraints          = txcore.TxConstraints
	TxTreeTupleNumElements = txcore.TxTreeTupleNumElements
)

const ConsumedOutputsBranch = txcore.ConsumedOutputsBranch

var (
	PathToRawTransaction     = txcore.PathToRawTransaction
	PathToConsumedOutputs    = txcore.PathToConsumedOutputs
	PathToTxVersion          = txcore.PathToTxVersion
	PathToTxConstraints      = txcore.PathToTxConstraints
	PathToProducedOutputs    = txcore.PathToProducedOutputs
	PathToUnlockParams       = txcore.PathToUnlockParams
	PathToInputIDs           = txcore.PathToInputIDs
	PathToEndorsements       = txcore.PathToEndorsements
	PathToSequencerDataBytes = txcore.PathToSequencerDataBytes
	PathToTimestamp          = txcore.PathToTimestamp
	PathToSignature          = txcore.PathToSignature
	PathToInputCommitment    = txcore.PathToInputCommitment
	PathToExplicitBaseline   = txcore.PathToExplicitBaseline
)

// Mandatory output block indices.
//
// Layout: [0] amounts, [1] index-value tuple, [2] lock, [3] chain (when
// present), [4] foundry (on foundry outputs), [5] foundryPolicy
// (optional, on foundry outputs), [6] delegationParams (optional, on
// chain outputs opting in to accept delegations).
// See claude/utxo-indexing.md §4, claude/native_token.md, and
// claude/delegation_epoch_params.md.
const (
	ConstraintIndexAmounts          = byte(iota) // 0
	ConstraintIndexIndexValues                   // 1: tuple of indexable values (controllers / target / sender hashes) for trie indexing
	ConstraintIndexLock                          // 2
	ConstraintIndexChain                         // 3 (when present)
	ConstraintIndexFoundry                       // 4 (on foundry outputs)
	ConstraintIndexFoundryPolicy                 // 5 (optional, on foundry outputs)
	ConstraintIndexDelegationParams              // 6 (optional, on chain outputs opting in to accept delegations)
)

func pathConstantsUpgrade0() string {
	return fmt.Sprintf(_pathConstantsJSON,
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
		ConstraintIndexAmounts,
		ConstraintIndexIndexValues,
		ConstraintIndexLock,
		ConstraintIndexChain,
		ConstraintIndexFoundry,
		ConstraintIndexFoundryPolicy,
		ConstraintIndexDelegationParams,
	)
}

// _pathConstantsJSON is consumed by easyfl.IntroduceUpdateJSONMulti.
// Numeric (%d) and hex-string (%s) placeholders are filled by pathConstantsUpgrade0.
const _pathConstantsJSON = `{
  "functions": [
    {"sym": "pathToTransaction",        "numArgs": 0, "source": "%d"},
    {"sym": "pathToTxVersion",          "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToTxConstraints",      "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToConsumedOutputs",    "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToProducedOutputs",    "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToUnlockParams",       "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToInputIDs",           "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToSignatureData",      "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToSequencerDataBytes", "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToInputCommitment",    "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToEndorsements",       "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToExplicitBaseline",   "numArgs": 0, "source": "0x%s"},
    {"sym": "pathToTimestamp",          "numArgs": 0, "source": "0x%s"},
    {"sym": "amountsConstraintIndex",          "numArgs": 0, "source": "%d"},
    {"sym": "indexValuesConstraintIndex",      "numArgs": 0, "source": "%d"},
    {"sym": "lockConstraintIndex",             "numArgs": 0, "source": "%d"},
    {"sym": "chainConstraintIndex",            "numArgs": 0, "source": "%d"},
    {"sym": "foundryConstraintIndex",          "numArgs": 0, "source": "%d"},
    {"sym": "foundryPolicyConstraintIndex",    "numArgs": 0, "source": "%d"},
    {"sym": "delegationParamsConstraintIndex", "numArgs": 0, "source": "%d"}
  ]
}
`
