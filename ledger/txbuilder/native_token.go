package txbuilder

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// MakeFoundryOriginOutput builds a fresh foundry origin output: PRXI
// amount + lock at index 2, chain origin at index 3, foundry(NilChainID,
// initialSupply) at index 4, and the optional raw policy bytecode at
// index 5. The chain ID is still NilChainID at origin — foundry()'s
// EasyFL body skips the tag-equals-chain-ID check at origin and starts
// enforcing it on the first transit, at which point the produced
// foundry's tag becomes the real chain ID.
//
// initialSupply at origin must typically be 0 (no real tag exists yet,
// so no tokenAmount outputs can carry the real tag in the origin tx).
// Minting happens at a later transit. foundry() does NOT enforce
// immutability of index 5 across transit; the policy script (if any)
// is responsible for self-locking via `selfImmutableOnSuccessorIndex`.
// MakeFoundryOriginOutput builds a foundry chain origin output.
// Foundries are never delegation targets — delegation targets are
// always sequencer chains. A foundry's controller may DELEGATE the
// foundry's token holdings to a sequencer by referencing the foundry
// chain ID as the master in a separate delegation UTXO (chainLock
// master path); the foundry chain output itself never carries
// delegationParams. See claude/delegation_epoch_params.md.
func MakeFoundryOriginOutput(amount uint64, lock ledger.Lock, originSlot uint32, initialSupply uint64, policyScript []byte) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(lock)
		o.PutConstraint(ledger.NewChainOrigin(originSlot).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundryOrigin(initialSupply).Bytes(), ledger.ConstraintIndexFoundry)
		if len(policyScript) > 0 {
			o.PutConstraint(policyScript, ledger.ConstraintIndexFoundryPolicy)
		}
	})
}

// TransitFoundry consumes a foundry chain output and produces a
// transited foundry output with the supply updated to newSupply. The
// produced foundry inherits the consumed output's lock, amounts and
// optional policy bytes at index 5. foundry() itself does NOT enforce
// immutability of index 5; if the consumed policy script self-locks
// (e.g. via `selfImmutableOnSuccessorIndex`), the unchanged carry-over
// here will satisfy that check.
//
// Side effects on the builder:
//   - the consumed foundry is added as an input;
//   - the produced foundry is added as an output;
//   - chain unlock parameters are wired between input and output;
//   - a `token(chainID, foundryProducedIdx)` constraint is pushed onto
//     the TxConstraints list — declaring the tag and the foundry transit
//     for Phase B's balance equation and Phase D's auditability check.
//
// Returns the produced output index.
func (txb *TxBuilder) TransitFoundry(inChainData *ledger.OutputDataWithChainID, newSupply uint64) (byte, error) {
	chainIN, err := ledger.OutputFromBytesWithLib(inChainData.Data, ledger.L(inChainData.ID.Slot()))
	if err != nil {
		return 0, fmt.Errorf("TransitFoundry: parse input: %w", err)
	}
	cc := chainIN.ChainConstraint()
	if cc == nil {
		return 0, fmt.Errorf("TransitFoundry: input is not a chained output")
	}
	if _, err := chainIN.ConstraintAt(ledger.ConstraintIndexFoundry); err != nil {
		return 0, fmt.Errorf("TransitFoundry: input has no foundry constraint at slot %d: %w", ledger.ConstraintIndexFoundry, err)
	}
	predecessorInputIdx, err := txb.ConsumeOutput(chainIN, inChainData.ID)
	if err != nil {
		return 0, err
	}
	successor := ledger.NewChainConstraint(
		inChainData.ChainID, predecessorInputIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	newFoundry := ledger.NewFoundry(inChainData.ChainID, newSupply)
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(newFoundry.Bytes(), ledger.ConstraintIndexFoundry)
		// Slot 5 (policy script) is carried over by Clone — foundry()
		// EasyFL enforces byte-equality across the transit.
	})
	producedIdx, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return 0, err
	}
	txb.PutUnlockParams(predecessorInputIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(producedIdx))
	txb.PushTxConstraint(ledger.TokenFoundryBytecode(inChainData.ChainID, producedIdx))
	return producedIdx, nil
}

// DeclareTokenConservation pushes a pure-conservation `token(tag, 0x)`
// onto TxConstraints — declaring the tag for Phase D auditability and
// asserting Σ consumed(tag) == Σ produced(tag) with no foundry transit.
// Use when a tx transfers an existing tag's tokenAmount instances
// between holders without minting or burning.
func (txb *TxBuilder) DeclareTokenConservation(tag base.ChainID) {
	txb.PushTxConstraint(ledger.TokenSentinelBytecode(tag))
}
