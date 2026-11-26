package tests

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/freezeburn"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/v3/factory"
	"github.com/stretchr/testify/require"
)

// Sequencers run across the slot of library upgrade 8 (kb/burn_frozen.md) with a burn set of
// two plain outputs, sent before the slot to a key nobody uses and frozen by freezeburn. The
// library's burned amount is set to their total, as the real one is to the real list. Checked:
//   - every committed branch before the slot holds both outputs and none at or after it does:
//     each crossing branch deleted them from its own baseline;
//   - a crossing branch's supply is its predecessor's plus its slot inflation less the burned
//     amount, and every other branch's is its predecessor's plus its slot inflation;
//   - branches keep coming after the slot and no milestone turns BAD.
//
// Upgrades 1 to 7 precede it at the minimum spacing, as in TestSequencersAcrossUpgrade7; the
// test runs about twelve minutes.
func TestBurnAcrossUpgrade8(t *testing.T) {
	const (
		upgrade1Slot = 10
		upgrade2Slot = upgrade1Slot + multistate.MinSlotsBetweenUpgrades
		upgrade3Slot = upgrade2Slot + multistate.MinSlotsBetweenUpgrades
		upgrade4Slot = upgrade3Slot + multistate.MinSlotsBetweenUpgrades
		upgrade5Slot = upgrade4Slot + multistate.MinSlotsBetweenUpgrades
		upgrade6Slot = upgrade5Slot + multistate.MinSlotsBetweenUpgrades
		upgrade7Slot = upgrade6Slot + multistate.MinSlotsBetweenUpgrades
		upgradeSlot  = upgrade7Slot + multistate.MinSlotsBetweenUpgrades
		afterSlots   = 30
		burnEach     = 1_000 * base.PROX
		burnTotal    = 2 * burnEach
	)
	build6 := func(prev []byte) ([]byte, error) {
		lib, err := ledger.BuildUpgrade6LibraryAt(upgrade6Slot)(prev)
		if err != nil {
			return nil, err
		}
		return ledger.BuildUpgradeFromSource(lib, "test floor and short pause",
			fmt.Sprintf("func constMineFloorDifficulty : u64/6\nfunc constDisableMiningUntilSlot : u64/%d", upgrade6Slot+6))
	}
	build8 := func(prev []byte) ([]byte, error) {
		lib, err := ledger.BuildUpgrade8LibraryAt(upgradeSlot)(prev)
		if err != nil {
			return nil, err
		}
		return ledger.BuildUpgradeFromSource(lib, "test burn set", fmt.Sprintf("func constBurnedAmount : u64/%d", uint64(burnTotal)))
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
		{Slot: upgrade7Slot, Build: ledger.BuildUpgrade7LibraryAt(upgrade7Slot)},
		{Slot: upgradeSlot, Build: build8},
	})
	savedScoreSlot := factory.ScoreActivationSlot
	factory.ScoreActivationSlot = 0
	t.Cleanup(func() { factory.ScoreActivationSlot = savedScoreSlot })

	versions := []string{"v3", "v3", "v3"}
	testData := initMultiSequencerTest(t, len(versions), true)
	testData.seqVersions = versions
	testData.env.lrbFromStore.Store(true)
	require.True(t, ledger.L(upgradeSlot).HasUpgrade8())
	require.False(t, ledger.L(upgradeSlot-1).HasUpgrade8())

	// the burn set: two transfers from the faucet to a key nobody uses, tagged along to the
	// bootstrap sequencer so they are included
	par := &spammerParams{
		t:             t,
		privateKey:    testData.privKeyFaucet,
		remainder:     testData.faucetOutput,
		tagAlongSeqID: []base.ChainID{testData.bootstrapChainID},
		target:        testData.addrAux,
		pace:          30,
		batchSize:     2,
		sendAmount:    burnEach,
		tagAlongFee:   base.PROX,
	}
	burnSet := make(map[base.OutputID]uint64)
	var lastTxID base.TransactionID
	for _, txBytes := range makeTransfers(par) {
		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)
		tx.ForEachProducedOutput(func(i byte, o *ledger.Output, oid base.OutputID) bool {
			if o.Lock().String() == testData.addrAux.String() && o.TokenBalance() == burnEach {
				burnSet[oid] = burnEach
			}
			return true
		})
		lastTxID, err = testData.wrk.TxBytesInForTests(txBytes)
		require.NoError(t, err)
	}
	require.Len(t, burnSet, 2)
	t.Cleanup(freezeburn.SetBurnedForTesting(burnSet))

	testData.startSequencersWithTimeout(1000)
	_, err := testData.wrk.WaitUntilTransactionInHeaviestState(lastTxID, 60*time.Second)
	require.NoError(t, err)
	require.Less(t, ledger.TimeNow().Slot, uint32(upgradeSlot), "the burn set must be committed before the upgrade slot")

	var msMutex sync.Mutex
	var milestones []*vertex.WrappedTx
	for _, seq := range append([]testSequencer{testData.bootstrapSeq}, testData.sequencers...) {
		seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
			msMutex.Lock()
			milestones = append(milestones, ms)
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
	held := func(rdr *multistate.Readable) int {
		n := 0
		for oid := range burnSet {
			if rdr.HasUTXO(oid) {
				n++
			}
		}
		return n
	}
	crossing, branchesAfter := 0, 0
	for slot := uint32(1); slot <= multistate.FetchLatestCommittedSlot(store); slot++ {
		for _, rr := range multistate.FetchRootRecords(store, slot) {
			rdr, err := multistate.NewReadable(store, rr.Root)
			require.NoError(t, err)
			bd := multistate.FetchBranchDataByRoot(store, rr)
			stemLock, ok := bd.Stem.Output.StemLock()
			require.True(t, ok)
			pred, ok := multistate.FetchBranchData(store, stemLock.PredecessorOutputID.TransactionID())
			if slot >= upgradeSlot {
				require.Zero(t, held(rdr), "a branch of slot %d still holds burned outputs", slot)
				branchesAfter++
			}
			if !ok {
				continue
			}
			want := pred.Supply + bd.SlotInflation
			if pred.Stem.ID.Slot() < upgradeSlot && slot >= upgradeSlot {
				crossing++
				want -= burnTotal
				predRdr, err := multistate.NewReadable(store, pred.Root)
				require.NoError(t, err)
				require.Equal(t, len(burnSet), held(predRdr), "the baseline of crossing branch %s lacks burned outputs", bd.Stem.ID.StringShort())
			}
			require.Equal(t, want, bd.Supply, "supply of branch %s in slot %d", bd.Stem.ID.StringShort(), slot)
		}
	}
	t.Logf("crossing branches: %d, branches at or after the upgrade slot %d: %d", crossing, upgradeSlot, branchesAfter)
	require.Positive(t, crossing)
	require.Greater(t, branchesAfter, afterSlots/2, "branches must keep coming after the upgrade slot")

	msMutex.Lock()
	defer msMutex.Unlock()
	bad := 0
	for _, ms := range milestones {
		if ms.GetTxStatus() == vertex.Bad {
			bad++
			t.Logf("BAD milestone %s: %v", ms.IDShortString(), ms.GetError())
		}
	}
	t.Logf("milestones observed: %d, BAD: %d", len(milestones), bad)
	require.Zero(t, bad, "no milestone may turn BAD")
}
