package txbuildercore

import "github.com/lunfardo314/proxima/ledger/base"

// ChainKind classifies a chained output from the wallet's perspective.
// Computed purely from raw output bytes + the wallet library.
type ChainKind int

const (
	// ChainKindNone — the output is not a chain (no chain constraint at
	// ConstraintIndexChain).
	ChainKindNone ChainKind = iota
	// ChainKindOther — a chain output that is none of the recognised
	// kinds below (a plain/generic chain).
	ChainKindOther
	// ChainKindSequencer — sequencer constraint at ConstraintIndexFoundry.
	ChainKindSequencer
	// ChainKindFoundry — foundry constraint at ConstraintIndexFoundry.
	ChainKindFoundry
	// ChainKindDelegation — delegate lock at ConstraintIndexLock.
	ChainKindDelegation
)

// ClassifyChain returns the chain kind of an output, computed from raw
// bytes + the wallet library. Singleton-free. Discriminator priority
// mirrors the server-side classifier (api/chain_explorer.makeRow):
// chain constraint presence first, then sequencer/foundry at index 4
// (mutually exclusive), then a delegate lock at index 2, else generic.
//
// Classifying by the output's own constraints — not by
// oid.IsSequencerTransaction() — is the correct test: a delegation
// transition carried inside its target sequencer's transaction sets the
// output ID's sequencer bit but adds no sequencer constraint.
func (l *Library[any]) ClassifyChain(o *Output, oid base.OutputID) ChainKind {
	chainBin, err := o.ConstraintAt(ConstraintIndexChain)
	if err != nil || len(chainBin) == 0 {
		return ChainKindNone
	}
	if _, err := l.ParseChainConstraint(chainBin); err != nil {
		return ChainKindNone
	}
	// index 4 is shared by sequencer() and foundry() (mutually exclusive).
	if seqBytes, err := o.ConstraintAt(ConstraintIndexFoundry); err == nil && len(seqBytes) > 0 {
		if _, err := l.ParseSequencerConstraint(seqBytes); err == nil {
			return ChainKindSequencer
		}
		if _, err := l.ParseFoundryBytecode(seqBytes); err == nil {
			return ChainKindFoundry
		}
	}
	if _, isDlg, err := l.ParseDelegationOutput(o, oid); err == nil && isDlg {
		return ChainKindDelegation
	}
	return ChainKindOther
}
