package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
)

const (
	BootstrapSequencerName = "boot"
	// BoostrapSequencerIDHex is a constant
	BoostrapSequencerIDHex = "8739faa34a6902e49bc16455bbd642fd3c649e8959d97089e43f214ca57ea0e5"
)

func GenesisOutput(initialSupply uint64, controllerAddress AddressED25519) *OutputWithChainID {
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
