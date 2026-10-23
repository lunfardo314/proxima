package ledger

import "github.com/lunfardo314/proxima/ledger/base"

// CurrentSlotOutputCoverage weights an unspent output in a natively validated
// state. Only the branch's transitioning sequencer output is exceptional: its
// inflation counts immediately, its capital/frozen delegation weight does not.
// Additional chain origins or wallet outputs of a branch retain normal weight.
func CurrentSlotOutputCoverage(o *Output, id base.OutputID, at base.LedgerTime) uint64 {
	if id.Slot() != at.Slot || id.Timestamp().After(at) {
		return 0
	}
	if id.IsBranchTransaction() && o.IsSequencerOutput() {
		if cc := o.ChainConstraint(); cc != nil && !cc.IsOrigin() {
			return o.Inflation()
		}
	}
	return Coverage(o, id, at)
}

// BuildSlotLocalOutputCoverageLibrary prepares the proposed consensus library.
// It does not schedule activation on any network. Existing libraries are unchanged.
func BuildSlotLocalOutputCoverageLibrary(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "slot-local output coverage", "func constSlotLocalOutputCoverage : u64/1")
}
