package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// Library upgrade 4 end to end (kb/library_upgrade4.md): listed after upgrades 1 to 3 and
// applied by the path a node takes at start, then transactions are validated on both sides
// of its slot. The two rules: every endorsement is a sequencer transaction, and the sequencer
// output of a sequencer transaction is not a chain origin.

// upgrade4Slot is where these tests activate upgrade 4: past upgrade 3's slot by at least the
// minimum spacing between upgrades.
const upgrade4Slot = 310

// withUpgrade4 lists upgrades 1 to 4, applies them to the test ledger's store as a node does
// at start, and restores the package ledger afterwards.
func withUpgrade4(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// settleWalletSequencerOrigin makes a sequencer chain origin the way it is meant to be made, by
// a wallet transaction (no sequencer data, no endorsements), settles it and returns the chain
// output and its chain ID.
func (e *sequencerTestEnv) settleWalletSequencerOrigin(t *testing.T, originTs base.LedgerTime) (*ledger.OutputWithID, base.ChainID) {
	t.Helper()
	outs := getSourceOutputs(t, e.u, e.addr)
	txb := exhelp.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	for i := 1; i < len(outs); i++ {
		require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
	}
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(e.addr)
		o.MustPushConstraint(ledger.NewChainOrigin(originTs.Slot).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
	}))
	require.NoError(t, err)
	txb.SetTimestamp(originTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)
	txBytes := txb.Bytes()
	require.NoError(t, e.u.AddTransaction(txBytes))

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(tx.ID(), 0))
	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	chainIn, err := chs.Parse()
	require.NoError(t, err)
	return chainIn, chainID
}

// The upgraded library carries upgrade 4 from its slot on and not before, and the upgrade index
// reaches the wallet constants.
func TestUpgrade4Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade4(t, u)

	require.False(t, ledger.L(upgrade4Slot-1).HasUpgrade4())
	require.True(t, ledger.L(upgrade4Slot).HasUpgrade4())
	require.EqualValues(t, 4, ledger.L(upgrade4Slot).Constants.UpgradeIndex)
	// earlier upgrades' rules carry over
	require.EqualValues(t, 9, constAt(t, upgrade4Slot, "constPostBranchConsolidationTicks"))
}

// A sequencer transaction whose sequencer output is a chain origin is valid before the slot and
// refused from it. A chain origin made by a wallet transaction stays valid.
func TestUpgrade4SequencerOutputNotOrigin(t *testing.T) {
	cases := []struct {
		name  string
		ts    base.LedgerTime
		error string // "" when valid
	}{
		{"before the slot", base.T(upgrade4Slot-10, 20), ""},
		{"from the slot", base.T(upgrade4Slot, 20), "the sequencer output of a sequencer transaction cannot be a chain origin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, 10_000_000_000)
			withUpgrade4(t, e.u)
			err := e.u.AddTransaction(e.buildSequencerOrigin(t, c.ts))
			if c.error == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NoError(t, util.MustErrorWith(err, c.error))
			}
		})
	}
	t.Run("wallet origin, from the slot", func(t *testing.T) {
		e := newSequencerTestEnv(t, 10_000_000_000)
		withUpgrade4(t, e.u)
		e.settleWalletSequencerOrigin(t, base.T(upgrade4Slot, 20))
	})
}

// A milestone endorsing a non-sequencer transaction, at the first or a later index, is valid
// before the slot and refused from it; one endorsing sequencer transactions only stays valid.
// The endorsed IDs are synthetic: endorsement targets are not looked up by the ledger, only
// their IDs are checked.
func TestUpgrade4Endorsements(t *testing.T) {
	// a cross-slot successor of a wallet-made origin of the previous slot, endorsing IDs of its
	// own slot one tick earlier; seqFlags gives the sequencer flag of each endorsed ID
	milestone := func(t *testing.T, succSlot uint32, seqFlags ...bool) error {
		e := newSequencerTestEnv(t, 10_000_000_000)
		withUpgrade4(t, e.u)
		chainIn, chainID := e.settleWalletSequencerOrigin(t, base.T(succSlot-1, 20))
		endorse := make([]base.TransactionID, len(seqFlags))
		for i, seq := range seqFlags {
			endorse[i] = base.NewTransactionID(base.T(succSlot, 29), base.TransactionIDShort{byte(i + 1)}, seq)
		}
		succ, _ := e.buildSequencerSuccessor(t, chainIn, chainID, base.T(succSlot, 30), endorse)
		return e.u.AddTransaction(succ)
	}
	const refused = "only sequencer transactions can be endorsed"

	t.Run("sequencer endorsements, from the slot", func(t *testing.T) {
		require.NoError(t, milestone(t, upgrade4Slot+5, true, true))
	})
	t.Run("non-sequencer first endorsement, before the slot", func(t *testing.T) {
		require.NoError(t, milestone(t, upgrade4Slot-10, false))
	})
	t.Run("non-sequencer first endorsement, from the slot", func(t *testing.T) {
		err := milestone(t, upgrade4Slot+5, false)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, refused))
	})
	t.Run("non-sequencer later endorsement, before the slot", func(t *testing.T) {
		require.NoError(t, milestone(t, upgrade4Slot-10, true, true, false))
	})
	t.Run("non-sequencer later endorsement, from the slot", func(t *testing.T) {
		err := milestone(t, upgrade4Slot+5, true, true, false)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, refused))
	})
}
