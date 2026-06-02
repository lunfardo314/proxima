package txbuildercore

import "github.com/lunfardo314/easyfl/tuples"

// Top-level tuple indices for a Proxima transaction value. A transaction
// is a 2-element tuple: TransactionTuple (the tx itself) and
// ConsumedTuple (the consumed-outputs branch).
const (
	TransactionTuple byte = iota
	ConsumedTuple
)

// Transaction subtree indices. These define the wire format of the
// transaction tuple — they are baked into the persisted ledger state and
// must not change without a coordinated upgrade.
const (
	TxVersion            byte = iota // uint16 BE: library upgrade index for this tx's slot
	TxTimestamp                      // 5-byte ledger timestamp
	TxSequencerDataBytes             // 4-byte sequencer info (omitted on non-sequencer txs)
	TxSignatureData                  // ed25519 signature + pubkey
	TxInputCommitment                // blake2b hash of consumed outputs
	TxExplicitBaseline               // optional explicit baseline branch txID
	TxInputIDs                       // tuple of consumed output IDs
	TxUnlockData                     // tuple of unlock-params blocks
	TxOutputs                        // tuple of produced output bytes
	TxEndorsements                   // tuple of endorsed txIDs
	TxConstraints                    // tuple of tx-level constraint bytecodes
	TxTreeTupleNumElements           // number of slots in the tx subtree (sentinel)
)

// ConsumedOutputsBranch is the inner-tuple index inside ConsumedTuple
// holding the consumed-outputs slice.
const ConsumedOutputsBranch = byte(0)

// Path constants for navigating into a parsed transaction tree.
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
)
