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
	BoostrapSequencerIDHex = "adffaebe21679c48ea416ac1bff6bce817c84648cd5c7d59"
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

// GenesisMineChainDust is the fixed token balance C of the mine chain output.
// mineLock pins every successor's balance equal to C, so C must satisfy the
// minimum storage deposit for the largest size the mine output ever reaches
// (its slot-ring args grow as slots climb). The storage-deposit schedule is
// size-only (not supply-relative), and the mine output stays well under 256
// bytes for its whole life; storageDeposit(256) ≈ 44M, so 50M is a safe fixed
// bound. Carved out of the genesis output so total genesis supply stays
// constInitialSupply. See claude/fairlaunch.md.
const GenesisMineChainDust = 50_000_000

// GenesisMineChainOutput builds the fair-launch mine chain output (genesis
// index 3): an open mineLock carrying R_init and the seed difficulty B0, and a
// chain origin whose chain ID is the constant MineChainID.
func GenesisMineChainOutput() *OutputWithChainID {
	lib := L(0)
	rInit, err := _uint64FromConst(lib.Library, "constMineRemainingInit")
	util.AssertNoError(err)
	b0, err := _uint64FromConst(lib.Library, "constMineBaseDifficulty")
	util.AssertNoError(err)
	ret := &OutputWithChainID{
		OutputWithID: OutputWithID{
			ID: base.GenesisMineChainOutputID(),
			Output: NewOutput(func(o *OutputBuilder) {
				o.WithAmounts(int64(GenesisMineChainDust)).WithLock(NewMineLock(rInit, b0, 0, 0, 0))
				o.PutConstraint(NewChainOrigin(0).Bytes(), ConstraintIndexChain)
			}),
		},
		ChainConstraintData: ChainConstraintData{
			ChainConstraint: ChainConstraint{
				ChainID: base.MineChainID,
			},
		},
	}
	util.Assertf(GenesisMineChainDust >= lib.MinimumStorageDeposit(ret.Output),
		"GenesisMineChainDust must cover the mine output storage deposit")
	return ret
}

func GenesisStemOutput() *OutputWithID {
	// Genesis stem aggregates:
	//   TotalSupply   = constInitialSupply
	//   TotalCoverage = TotalSupply
	//   SlotInflation = 0
	//   OracleData (index 3): FrozenCoverage / NumConfirmedTransactions /
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
			o.PutConstraint((&OracleData{
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
