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
	BoostrapSequencerIDHex = "9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a825e31496c08ef8d6d"
)

func GenesisOutput(initialSupply uint64, controllerAddress SigLock) *OutputWithChainID {
	oid := base.GenesisOutputID()
	return &OutputWithChainID{
		OutputWithID: OutputWithID{
			ID: oid,
			Output: NewOutput(func(o *OutputBuilder) {
				o.WithAmounts(int64(initialSupply)).WithLock(controllerAddress)
				chainIdx := o.MustPushConstraint(NewChainOrigin(0, initialSupply).Bytes())
				o.MustPushConstraint(NewSequencerConstraint(chainIdx).Bytes())

				msData := seqdata.New()
				msData.SetName(BootstrapSequencerName)
				idxMsData := o.MustPushConstraint(easyfl.InlineDataBytecode(msData.Bytes()))
				util.Assertf(idxMsData == SeqMilestoneDataFixedIndex, "idxMsData == SeqMilestoneDataFixedIndex")
			}),
		},
		ChainConstraintData: ChainConstraintData{
			ChainConstraint: ChainConstraint{
				ChainID:      base.BoostrapSequencerID,
				OriginAmount: initialSupply,
			},
			ChainConstraintIndex: 2,
		},
	}
}

func GenesisStemOutput() *OutputWithID {
	return &OutputWithID{
		ID: base.GenesisStemOutputID(),
		Output: NewOutput(func(o *OutputBuilder) {
			o.WithAmounts(0).
				WithLock(&StemLock{
					PredecessorOutputID: base.OutputID{},
				})
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
