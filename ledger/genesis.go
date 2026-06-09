package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
)

const (
	BootstrapSequencerName = "boot"
	// BoostrapSequencerIDHex is a constant (must match base.BoostrapSequencerIDHex)
	BoostrapSequencerIDHex = "9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82"
)

func GenesisOutput(initialSupply uint64, controllerAddress SigLock) *OutputWithChainID {
	oid := base.GenesisOutputID()
	lib := L(0)
	return &OutputWithChainID{
		OutputWithID: OutputWithID{
			ID: oid,
			Output: NewOutput(func(o *OutputBuilder) {
				o.WithAmounts(int64(initialSupply)).WithLock(controllerAddress)
				o.PutConstraint(NewChainOrigin(0).Bytes(), ConstraintIndexChain)
				// Sequencer constraint carries the delegation params
				// (epochSlots, maxFrozenEpochs) directly — chain type is
				// fixed at origin to "sequencer chain that always
				// accepts delegations with these immutable params".
				// coverageDelta = initialSupply: the genesis branch's coverage
				// delta. The stem's total-coverage recurrence reads it here, and
				// BranchData projects it from this constraint (see state.go).
				idxSeq := o.MustPushConstraint(
					NewSequencerConstraint(lib.DelegationEpochSlots, byte(lib.MaxFrozenEpochs), initialSupply).Bytes())
				util.Assertf(idxSeq == SequencerConstraintFixedIndex, "idxSeq == SequencerConstraintFixedIndex")

				msData := seqdata.New()
				msData.SetName(BootstrapSequencerName)
				idxMsData := o.MustPushConstraint(easyfl.InlineDataBytecode(msData.Bytes()))
				util.Assertf(idxMsData == SeqMilestoneDataFixedIndex, "idxMsData == SeqMilestoneDataFixedIndex")
			}),
		},
		ChainConstraintData: ChainConstraintData{
			ChainConstraint: ChainConstraint{
				ChainID: base.BoostrapSequencerID,
			},
		},
	}
}

func GenesisStemOutput() *OutputWithID {
	// Genesis stem aggregates:
	//   TotalSupply   = constInitialSupply
	//   TotalCoverage = TotalSupply
	//   SlotInflation = 0
	//   StemData (index 3): FrozenCoverage / NumConfirmedTransactions /
	//             NumSeqTransactions / NumSeq = 0, BaselineRoot = TrieHashSize zero bytes
	// coverageDelta = initialSupply now lives on the genesis sequencer output's
	// sequencer constraint (see GenesisOutput), not on the stem.
	initialSupply := L(0).InitialSupply
	return &OutputWithID{
		ID: base.GenesisStemOutputID(),
		Output: NewOutput(func(o *OutputBuilder) {
			o.WithAmounts(0).
				WithLock(&StemLock{
					PredecessorOutputID: base.OutputID{},
					TotalSupply:         initialSupply,
					TotalCoverage:       initialSupply,
				})
			o.PutConstraint((&StemData{
				BaselineRoot: make([]byte, TrieHashSize),
			}).Bytes(), ConstraintIndexChain)
		}),
	}
}

// GenesisControllerDustOutput creates a minimal output for the controller's wallet.
// This ensures the controller always has at least one output to create transactions
// (e.g., withdraw requests from their sequencer when wallet is otherwise empty).
func GenesisControllerDustOutput(controllerAddress SigLock) *OutputWithID {
	return &OutputWithID{
		ID: base.GenesisControllerDustOutputID(),
		Output: NewOutput(func(o *OutputBuilder) {
			o.WithTokenBalance(1).WithLock(controllerAddress)
		}),
	}
}
