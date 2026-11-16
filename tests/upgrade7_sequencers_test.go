package tests

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/v3/factory"
	"github.com/stretchr/testify/require"
)

// Sequencers, all v3 (v2 is removed with upgrade 7: it ranked by classical coverage, which keeps the
// bonus as tokens, and would have favoured the larger bonus), all producing branches, run across the slot of library
// upgrade 7 (.internal/library_upgrade7.md). Checked:
//   - every branch at or after the slot holds the upgrade UTXO, and branches keep coming: the
//     sequencer's builder writes the bid commitment on every milestone and reveals it in the
//     branch, so the sealed bid, the chosen bonus and the two branch inputs pass the ledger;
//   - no milestone of any sequencer turns BAD;
//   - every branch from the slot on mints the bonus its sequencer committed to, the default 5 PROX
//     (no sequencer sets the `bonus` key here);
//   - from the slot no sequencer transaction other than a branch is stamped after tick 119;
//   - a fourth sequencer, v3, holding 15,000 PROX of its own and nothing delegated, under both
//     bounds of upgrade 7 but above upgrade 6's 10,000, branches before the slot and not after it,
//     keeps issuing milestones, and none of them turns BAD.
//
// Upgrade 6 is patched as in TestSequencersAcrossUpgrade6 (test mining floor, short pause). v3
// ranks by its own score from slot 0. Upgrades 1 to 6 precede it at the minimum spacing; the
// test runs about ten minutes.
func TestSequencersAcrossUpgrade7(t *testing.T) {
	const (
		upgrade1Slot = 10
		upgrade2Slot = upgrade1Slot + multistate.MinSlotsBetweenUpgrades
		upgrade3Slot = upgrade2Slot + multistate.MinSlotsBetweenUpgrades
		upgrade4Slot = upgrade3Slot + multistate.MinSlotsBetweenUpgrades
		upgrade5Slot = upgrade4Slot + multistate.MinSlotsBetweenUpgrades
		upgrade6Slot = upgrade5Slot + multistate.MinSlotsBetweenUpgrades
		upgradeSlot  = upgrade6Slot + multistate.MinSlotsBetweenUpgrades
		afterSlots   = 30
	)
	build6 := func(prev []byte) ([]byte, error) {
		lib, err := ledger.BuildUpgrade6LibraryAt(upgrade6Slot)(prev)
		if err != nil {
			return nil, err
		}
		return ledger.BuildUpgradeFromSource(lib, "test floor and short pause",
			fmt.Sprintf("func constMineFloorDifficulty : u64/6\nfunc constDisableMiningUntilSlot : u64/%d", upgrade6Slot+6))
	}
	extraLedgerOptions = []ledger.ParametersOption{ledger.WithMineDifficulty(6, 6, 10, 1)}
	t.Cleanup(func() { extraLedgerOptions = nil })
	withUnfoldedGenesis(t, []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: build6},
		{Slot: upgradeSlot, Build: ledger.BuildUpgrade7LibraryAt(upgradeSlot)},
	})
	savedScoreSlot := factory.ScoreActivationSlot
	factory.ScoreActivationSlot = 0
	t.Cleanup(func() { factory.ScoreActivationSlot = savedScoreSlot })

	versions := []string{"v3", "v3", "v3"}
	const smallBalance = 15_000 * base.PROX
	chainOriginAmounts = []uint64{initBalance, initBalance, initBalance, smallBalance}
	t.Cleanup(func() { chainOriginAmounts = nil })
	testData := initMultiSequencerTest(t, len(versions)+1, true)
	testData.seqVersions = append(append([]string{}, versions...), "v3")
	testData.env.lrbFromStore.Store(true)

	lib := ledger.L(upgradeSlot)
	require.True(t, lib.HasUpgrade7())
	require.False(t, ledger.L(upgradeSlot-1).HasUpgrade7())
	require.EqualValues(t, 119, lib.LastNonBranchTick())
	require.Greater(t, lib.MinSequencerBalance, uint64(smallBalance))
	require.Less(t, ledger.L(upgradeSlot-1).MinSequencerBalance, uint64(smallBalance))

	testData.startSequencersWithTimeout(1000)
	smallSeq := testData.sequencers[len(versions)]
	smallID := smallSeq.SequencerID()
	var msMutex sync.Mutex
	var milestones, smallMilestones []*vertex.WrappedTx
	for _, seq := range append([]testSequencer{testData.bootstrapSeq}, testData.sequencers...) {
		small := seq == smallSeq
		seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
			msMutex.Lock()
			milestones = append(milestones, ms)
			if small {
				smallMilestones = append(smallMilestones, ms)
			}
			msMutex.Unlock()
		})
	}
	deadline := time.Now().Add(time.Duration(upgradeSlot+afterSlots+40) * ledger.SlotDuration())
	for ledger.TimeNow().Before(base.T(upgradeSlot+afterSlots, 0)) {
		require.True(t, time.Now().Before(deadline), "the ledger clock did not reach the end of the test")
		time.Sleep(ledger.SlotDuration())
	}
	require.True(t, testData.stopAndWait(10*time.Second))

	store := testData.env.StateStore()
	upgradeOID := base.UpgradeOutputID(upgradeSlot)
	branchesAfter, smallBranchesBefore := 0, 0
	for slot := uint32(1); slot <= multistate.FetchLatestCommittedSlot(store); slot++ {
		for _, rr := range multistate.FetchRootRecords(store, slot) {
			if slot < upgradeSlot {
				if rr.SequencerID == smallID {
					smallBranchesBefore++
				}
				continue
			}
			rdr, err := multistate.NewReadable(store, rr.Root)
			require.NoError(t, err)
			require.True(t, rdr.HasUTXO(upgradeOID), "a branch of slot %d lacks the upgrade UTXO", slot)
			require.NotEqual(t, smallID, rr.SequencerID, "the small sequencer committed a branch in slot %d", slot)
			branchesAfter++
		}
	}
	t.Logf("branches committed by the small sequencer before the upgrade slot: %d", smallBranchesBefore)
	t.Logf("branches committed at or after the upgrade slot %d: %d", upgradeSlot, branchesAfter)
	require.Positive(t, smallBranchesBefore, "the small sequencer must branch under upgrade 6's bound")
	require.Greater(t, branchesAfter, afterSlots/2, "branches must keep coming after the upgrade slot")

	msMutex.Lock()
	defer msMutex.Unlock()
	bad, branchesSeen := 0, 0
	for _, ms := range milestones {
		if ms.GetTxStatus() == vertex.Bad {
			bad++
			t.Logf("BAD milestone %s: %v", ms.IDShortString(), ms.GetError())
		}
		ts := ms.Timestamp()
		if ts.Slot < upgradeSlot {
			continue
		}
		if ms.IsBranchTransaction() {
			branchesSeen++
			require.EqualValues(t, seqdata.DefaultBranchBonus, ms.InflationAmount(),
				"branch %s does not mint the committed bonus", ms.IDShortString())
			continue
		}
		require.LessOrEqual(t, ts.Tick, ledger.L(ts.Slot).LastNonBranchTick(),
			"milestone %s is stamped in the quiet end of the slot", ms.IDShortString())
	}
	t.Logf("milestones observed: %d, BAD: %d, branches from the upgrade slot: %d", len(milestones), bad, branchesSeen)
	require.Zero(t, bad, "no milestone may turn BAD")
	require.Positive(t, branchesSeen)

	smallAfter := 0
	for _, ms := range smallMilestones {
		if ms.Timestamp().Slot < upgradeSlot {
			continue
		}
		smallAfter++
		require.False(t, ms.IsBranchTransaction(), "the small sequencer submitted branch %s", ms.IDShortString())
	}
	t.Logf("small sequencer milestones: %d in all, %d from the upgrade slot", len(smallMilestones), smallAfter)
	require.Positive(t, smallAfter, "the small sequencer must keep issuing milestones after the slot")
}
