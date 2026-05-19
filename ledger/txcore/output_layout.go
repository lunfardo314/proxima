package txcore

// Output tuple slot indices. Defines which constraint kind lives at
// which position inside a single Output's constraint tuple. Wire format;
// do not change without a coordinated ledger upgrade.
//
// Layout:
//   [0] amounts          (token balance, inflation, frozen-coverage)
//   [1] index-values     (controllers / target / sender hashes for trie indexing)
//   [2] lock             (unlock policy bytecode)
//   [3] chain            (when present — chain output)
//   [4] foundry          (foundry-only — circulating supply)
//   [5] foundryPolicy    (foundry-only, optional)
//   [6] delegationParams (chain-only, optional — accept-delegations params)
const (
	ConstraintIndexAmounts          byte = iota // 0
	ConstraintIndexIndexValues                  // 1
	ConstraintIndexLock                         // 2
	ConstraintIndexChain                        // 3
	ConstraintIndexFoundry                      // 4
	ConstraintIndexFoundryPolicy                // 5
	ConstraintIndexDelegationParams             // 6
)
