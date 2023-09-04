package genesis

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// InitLedgerState initializes origin ledger state in the empty store
// Returns root commitment to the genesis ledger state and genesis chainID
func InitLedgerState(par IdentityData, store general.StateStore) (core.ChainID, common.VCommitment) {
	batch := store.BatchedWriter()
	emptyRoot := immutable.MustInitRoot(batch, core.CommitmentModel, par.Bytes())
	err := batch.Commit()
	util.AssertNoError(err)

	trie, err := immutable.NewTrieUpdatable(core.CommitmentModel, store, emptyRoot)
	util.AssertNoError(err)

	genesisAddr := core.AddressED25519FromPublicKey(par.GenesisControllerPublicKey)
	gout := GenesisOutput(par.InitialSupply, genesisAddr, par.GenesisTimeSlot)
	gStemOut := GenesisStemOutput(par.InitialSupply, par.GenesisTimeSlot)

	// write genesis outputs
	err = state.UpdateTrie(trie, genesisUpdateCommands(&gout.OutputWithID, gStemOut))
	util.AssertNoError(err)

	batch = store.BatchedWriter()
	root := trie.Commit(batch)
	err = batch.Commit()
	util.AssertNoError(err)
	return gout.ChainID, root
}

func GenesisOutput(initialSupply uint64, controllerAddress core.AddressED25519, genesisSlot core.TimeSlot) *core.OutputWithChainID {
	oid := GenesisChainOutputID(genesisSlot)
	return &core.OutputWithChainID{
		OutputWithID: core.OutputWithID{
			ID: oid,
			Output: core.NewOutput(func(o *core.Output) {
				o.WithAmount(initialSupply).WithLock(controllerAddress)
				chainIdx, err := o.PushConstraint(core.NewChainOrigin().Bytes())
				util.AssertNoError(err)
				_, err = o.PushConstraint(core.NewSequencerConstraint(chainIdx, 0).Bytes())
				util.AssertNoError(err)
			}),
		},
		ChainID: core.OriginChainID(&oid),
	}
}

func GenesisStemOutput(initialSupply uint64, genesisTimeSlot core.TimeSlot) *core.OutputWithID {
	return &core.OutputWithID{
		ID: GenesisStemOutputID(genesisTimeSlot),
		Output: core.NewOutput(func(o *core.Output) {
			o.WithAmount(0).
				WithLock(core.NewStemLock(initialSupply, 0, core.OutputID{}))
		}),
	}
}

func genesisUpdateCommands(genesisOut, genesisStemOut *core.OutputWithID) []state.UpdateCmd {
	return []state.UpdateCmd{
		{
			ID:     &genesisOut.ID,
			Output: genesisOut.Output,
		},
		{
			ID:     &genesisStemOut.ID,
			Output: genesisStemOut.Output,
		},
	}
}

func GenesisTransactionID(genesisTimeSlot core.TimeSlot) *core.TransactionID {
	ret := core.NewTransactionID(core.MustNewLogicalTime(genesisTimeSlot, 0), core.All0TransactionHash, true, true)
	return &ret
}

func GenesisChainOutputID(e core.TimeSlot) (ret core.OutputID) {
	// we are placing sequencer flag = true into the genesis tx ID to please sequencer constraint
	// of the origin branch transaction. It is the only exception
	ret = core.NewOutputID(GenesisTransactionID(e), GenesisOutputIndex)
	return
}

func GenesisStemOutputID(e core.TimeSlot) (ret core.OutputID) {
	ret = core.NewOutputID(GenesisTransactionID(e), GenesisStemOutputIndex)
	return
}
