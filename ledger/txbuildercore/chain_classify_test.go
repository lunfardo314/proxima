package txbuildercore_test

// Wallet-side chain classification. ClassifyChain must agree with the
// server-side classifier (api/chain_explorer.makeRow) on what each output is,
// since `proxi node allchains` labels chains by its verdict alone.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestClassifyChain_Mine verifies the genesis mining chain — a chain
// constraint plus an open mineLock at the lock index — classifies as
// ChainKindMine, and that a plain sigLock-controlled chain carrying the same
// chain constraint does not.
func TestClassifyChain_Mine(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	mineOut := ledger.GenesisMineChainOutput()
	require.Equal(t, txbuildercore.ChainKindMine, lib.ClassifyChain(mineOut.Output.Output, mineOut.ID))

	// the same output's lock parses as a mineLock, so the kind is not a
	// coincidence of the chain constraint alone
	lockBin, err := mineOut.Output.ConstraintAt(ledger.ConstraintIndexLock)
	require.NoError(t, err)
	mv, err := lib.ParseMineLock(lockBin)
	require.NoError(t, err)
	require.NotZero(t, mv.R, "the genesis mine chain starts with R_init mintable motes")

	var holder base.HolderID
	for i := range holder {
		holder[i] = byte(i + 1)
	}

	// a sigLock-controlled chain with no role-typing constraint is generic
	generic := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(1_000_000_000).WithLock(ledger.SigLock(holder))
		o.PutConstraint(ledger.NewChainConstraint(base.MineChainID, 0, 0, 0, 0, 0, 0).Bytes(), ledger.ConstraintIndexChain)
	})
	// note the chain ID alone does not make it a mining chain: only the
	// mineLock does, and the constraint itself pins the two together on-ledger
	require.Equal(t, txbuildercore.ChainKindOther, lib.ClassifyChain(generic.Output, base.OutputID{}))

	// the genesis output is the bootstrap sequencer chain
	genesisOut := ledger.GenesisOutput(1_000_000_000, ledger.SigLock(holder))
	require.Equal(t, txbuildercore.ChainKindSequencer, lib.ClassifyChain(genesisOut.Output.Output, genesisOut.ID))

	// a plain non-chain output is not a chain at all
	plain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(1_000_000_000).WithLock(ledger.SigLock(holder))
	})
	require.Equal(t, txbuildercore.ChainKindNone, lib.ClassifyChain(plain.Output, base.OutputID{}))
}
