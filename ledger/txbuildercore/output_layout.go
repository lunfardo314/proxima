package txbuildercore

// Output tuple slot indices. Defines which constraint kind lives at
// which position inside a single Output's constraint tuple. Wire format;
// do not change without a coordinated ledger upgrade.
//
// Layout:
//   [0] amounts          (token balance, inflation, frozen-coverage)
//   [1] index-values     (controllers / target / sender hashes for trie indexing)
//   [2] lock             (unlock policy bytecode)
//   [3] chain            (when present — chain output)
//   [4] foundry / sequencer  (chain-type marker, mutually exclusive at
//                              origin: `foundry(supply)` for foundry
//                              chains, `sequencer(epochSlots,
//                              maxFrozenEpochs)` for sequencer chains;
//                              empty for regular chains)
//   [5] foundryPolicy    (foundry-only, optional)
//   [6..] freeform per-output extras (delegateLockState at the last
//         position on delegation outputs; sequencer milestone data on
//         sequencer milestones; etc.)
const (
	ConstraintIndexAmounts       byte = iota // 0
	ConstraintIndexIndexValues               // 1
	ConstraintIndexLock                      // 2
	ConstraintIndexChain                     // 3
	ConstraintIndexFoundry                   // 4 (= SequencerConstraintFixedIndex)
	ConstraintIndexFoundryPolicy             // 5
)
