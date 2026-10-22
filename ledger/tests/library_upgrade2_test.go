package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// Library upgrade 2 end to end (kb/library_upgrade2.md): listed after upgrade 1 and
// applied by the path a node takes at start, then sequencer transactions are validated
// on both sides of its slot. The two rules: no sequencer transaction other than a branch
// before the post-branch consolidation tick, and in the pre-branch consolidation zone a
// chain predecessor of the same slot.

// upgrade2Slot is where these tests activate upgrade 2: past upgrade 1's slot by at
// least the minimum spacing between upgrades.
const upgrade2Slot = 110

// withUpgrade2 lists upgrades 1 and 2, applies them to the test ledger's store as a node
// does at start, and restores the package ledger afterwards.
func withUpgrade2(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// The upgraded library: the post-branch consolidation ticks are 9 from the slot on and
// absent (0) before it, and reach the wallet constants.
func TestUpgrade2Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade2(t, u)

	require.EqualValues(t, 0, ledger.L(upgrade2Slot-1).PostBranchConsolidationTicks)
	require.EqualValues(t, 9, ledger.L(upgrade2Slot).PostBranchConsolidationTicks)
	require.EqualValues(t, 9, constAt(t, upgrade2Slot, "constPostBranchConsolidationTicks"))
	require.EqualValues(t, 2, ledger.L(upgrade2Slot).Constants.UpgradeIndex)
	// upgrade 1's rules carry over
	require.EqualValues(t, 5, constAt(t, upgrade2Slot, "constMinerVersion"))
}

// A sequencer chain origin, a produced sequencer output with no chain predecessor: before the
// upgrade it is valid at tick 5 and, with one input, in the pre-branch zone; from the upgrade
// it is refused before the post-branch consolidation tick and anywhere in the zone, and valid
// at tick 9.
func TestUpgrade2SequencerOrigin(t *testing.T) {
	cases := []struct {
		name  string
		ts    base.LedgerTime
		error string // "" when valid
	}{
		{"before the slot, tick 5", base.T(upgrade2Slot-10, 5), ""},
		{"before the slot, tick 110 (zone)", base.T(upgrade2Slot-10, 110), ""},
		{"from the slot, tick 5", base.T(upgrade2Slot, 5), "sequencer transaction violates post-branch consolidation ticks constraint"},
		{"from the slot, tick 8", base.T(upgrade2Slot+1, 8), "sequencer transaction violates post-branch consolidation ticks constraint"},
		{"from the slot, tick 9", base.T(upgrade2Slot+1, 9), ""},
		{"from the slot, tick 110 (zone)", base.T(upgrade2Slot+1, 110), "sequencer transaction violates pre-branch consolidation ticks constraint"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSequencerTestEnv(t, 10_000_000_000)
			withUpgrade2(t, e.u)
			err := e.u.AddTransaction(e.buildSequencerOrigin(t, c.ts))
			if c.error == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NoError(t, util.MustErrorWith(err, c.error))
			}
		})
	}
}

// In the pre-branch zone a single-input milestone extending a chain predecessor of an earlier
// slot is valid before the upgrade and refused from it; extending a predecessor of the same
// slot stays valid. Outside the zone a cross-slot successor is valid on both sides.
func TestUpgrade2PreBranchZonePredecessor(t *testing.T) {
	// cross-slot successor of an origin of the previous slot, endorsing a milestone of its own
	// slot as a slot's first milestone must
	crossSlot := func(t *testing.T, succSlot uint32, succTick byte) error {
		e := newSequencerTestEnv(t, 10_000_000_000)
		withUpgrade2(t, e.u)
		chainIn, chainID := e.settleSequencerOrigin(t, base.T(succSlot-1, 20))
		endorse := base.NewTransactionID(base.T(succSlot, succTick-1), base.TransactionIDShort{}, true)
		succ, _ := e.buildSequencerSuccessor(t, chainIn, chainID, base.T(succSlot, succTick), []base.TransactionID{endorse})
		return e.u.AddTransaction(succ)
	}
	t.Run("cross-slot, zone, before the slot", func(t *testing.T) {
		require.NoError(t, crossSlot(t, upgrade2Slot-10, 110))
	})
	t.Run("cross-slot, zone, from the slot", func(t *testing.T) {
		err := crossSlot(t, upgrade2Slot+5, 110)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "sequencer transaction violates pre-branch consolidation ticks constraint"))
	})
	t.Run("cross-slot, outside the zone, from the slot", func(t *testing.T) {
		require.NoError(t, crossSlot(t, upgrade2Slot+5, 50))
	})
	t.Run("same-slot, zone, from the slot", func(t *testing.T) {
		e := newSequencerTestEnv(t, 10_000_000_000)
		withUpgrade2(t, e.u)
		slot := uint32(upgrade2Slot + 5)
		chainIn, chainID := e.settleSequencerOrigin(t, base.T(slot, 20))
		succ, _ := e.buildSequencerSuccessor(t, chainIn, chainID, base.T(slot, 110), nil)
		require.NoError(t, e.u.AddTransaction(succ))
	})
}
