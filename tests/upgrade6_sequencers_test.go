package tests

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"math/bits"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/mine"
	"github.com/lunfardo314/proxima/sequencer/v3/factory"
	"github.com/stretchr/testify/require"
)

// Sequencers of the three versions, v1, v2 and v3, all producing branches, run across the slot
// of library upgrade 6 (kb/library_upgrade6_mining.md): the transaction ID commits to the signer,
// the mine chain's work becomes the transit's own ID, mining pauses after the slot, the sequencer
// pace rises to 6 ticks. Checked:
//   - every branch at or after the slot holds the upgrade UTXO, branches keep coming;
//   - no milestone of any sequencer turns BAD;
//   - a plain wallet transfer is confirmed before and after the slot (old and new ID);
//   - transaction-hash transits submitted through the input queue from the end of the pause on,
//     each paying its reward as a tag-along to one of the three sequencers in turn, are settled:
//     the mine chain advances, and each version includes at least one transit addressed to it.
//
// Upgrade 6 is patched for the test: the difficulty floor back to the test band, the pause a few
// slots instead of 1,000. v3 ranks by its own score from slot 0, not only after its live
// activation slot. Upgrades 1 to 5 precede it (upgrade 6 builds on upgrade 1's settlement
// window), 2 to 5 at the minimum spacing; the test runs about ten minutes.
func TestSequencersAcrossUpgrade6(t *testing.T) {
	const (
		upgrade1Slot = 10
		upgrade2Slot = upgrade1Slot + multistate.MinSlotsBetweenUpgrades
		upgrade3Slot = upgrade2Slot + multistate.MinSlotsBetweenUpgrades
		upgrade4Slot = upgrade3Slot + multistate.MinSlotsBetweenUpgrades
		upgrade5Slot = upgrade4Slot + multistate.MinSlotsBetweenUpgrades
		upgradeSlot  = upgrade5Slot + multistate.MinSlotsBetweenUpgrades
		pauseSlots   = 6
		resumeSlot   = upgradeSlot + pauseSlots
		miningSlots  = 24
	)
	build6 := func(prev []byte) ([]byte, error) {
		lib, err := ledger.BuildUpgrade6LibraryAt(upgradeSlot)(prev)
		if err != nil {
			return nil, err
		}
		return ledger.BuildUpgradeFromSource(lib, "test floor and short pause",
			fmt.Sprintf("func constMineFloorDifficulty : u64/6\nfunc constDisableMiningUntilSlot : u64/%d", resumeSlot))
	}
	// a seed difficulty the test can solve within a slot while signing every attempt; set
	// before withUnfoldedGenesis, whose clean-up runs last and reinitializes the ledger
	extraLedgerOptions = []ledger.ParametersOption{ledger.WithMineDifficulty(6, 6, 10, 1)}
	t.Cleanup(func() { extraLedgerOptions = nil })
	withUnfoldedGenesis(t, []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgradeSlot, Build: build6},
	})
	savedScoreSlot := factory.ScoreActivationSlot
	factory.ScoreActivationSlot = 0
	t.Cleanup(func() { factory.ScoreActivationSlot = savedScoreSlot })

	versions := []string{"v1", "v2", "v3"}
	testData := initMultiSequencerTest(t, len(versions), true)
	testData.seqVersions = versions
	testData.env.lrbFromStore.Store(true)

	lib := ledger.L(upgradeSlot)
	require.EqualValues(t, ledger.L(upgradeSlot-1).UpgradeIndex()+1, lib.UpgradeIndex())
	require.True(t, lib.MineWorkIsTxID())
	require.EqualValues(t, resumeSlot, lib.DisableMiningUntilSlot)
	require.EqualValues(t, 6, lib.TransactionPaceSequencer)

	testData.startSequencersWithTimeout(1000)
	var msMutex sync.Mutex
	var milestones []*vertex.WrappedTx
	for _, seq := range append([]testSequencer{testData.bootstrapSeq}, testData.sequencers...) {
		seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
			msMutex.Lock()
			milestones = append(milestones, ms)
			msMutex.Unlock()
		})
	}
	deadline := time.Now().Add(time.Duration(resumeSlot+miningSlots+40) * ledger.SlotDuration())
	waitUntil := func(ts base.LedgerTime) {
		for ledger.TimeNow().Before(ts) {
			require.True(t, time.Now().Before(deadline), "the ledger clock did not reach %s", ts.String())
			time.Sleep(ledger.TickDuration())
		}
	}

	// a plain transfer from the faucet, tagged along to the bootstrap sequencer
	remainder := testData.faucetOutput
	transfer := func() base.TransactionID {
		ts := base.MaximumTime(ledger.TimeNow(), remainder.Timestamp().AddTicks(int(ledger.L(base.MaxSlot).TransactionPace))).AddTicks(1)
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}
		txBytes, rem, err := utxodb.MakeSimpleTransferTransactionWithRemainder(
			utxodb.NewTransferData(testData.privKeyFaucet, testData.addrFaucet, ts).
				MustWithInputs(remainder).
				WithTargetLock(testData.addr).
				WithAmount(1_000_000_000).
				WithTagAlong(testData.bootstrapChainID, tagAlongFee))
		require.NoError(t, err)
		remainder = rem
		txid, err := testData.wrk.TxBytesInForTests(txBytes)
		require.NoError(t, err)
		return txid
	}
	waitUntil(base.T(upgradeSlot-4, 20))
	transferBefore := transfer()
	waitUntil(base.T(upgradeSlot+2, 20))
	transferAfter := transfer()

	// one transit a slot, stamped at the earliest tick the ledger allows, its reward a
	// tag-along to the sequencers in turn. Nobody mined before, so the first transit spends
	// the genesis mine output, which no stored transaction produced: a sequencer's mine
	// registry must be able to validate a transit against it
	_, minerKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	type submitted struct {
		txid    base.TransactionID
		version string
	}
	var transits []submitted
	initialMine, err := testData.wrk.HeaviestStateForLatestTimeSlot().GetChainOutputWithID(base.MineChainID)
	require.NoError(t, err)
	require.NotNil(t, testData.env.GetLatestReliableBranch(), "the input queue proves a transit's predecessor against the LRB")
	for i := 0; i < miningSlots; i++ {
		slot := uint32(resumeSlot + i)
		slotLib := ledger.L(slot)
		ts := base.T(slot, slotLib.MineSettlementTick()-slotLib.TransactionPaceSequencer)
		waitUntil(ts)
		pred, err := testData.wrk.HeaviestStateForLatestTimeSlot().GetChainOutputWithID(base.MineChainID)
		require.NoError(t, err)
		target := i % len(versions)
		txBytes := buildTestHashTransit(t, pred, minerKey, ts, testData.sequencers[target].SequencerID())
		// valid in full against its predecessor, as the input queue checks it
		vtx, err := transaction.Parse(txBytes)
		require.NoError(t, err)
		require.NoError(t, vtx.SetFullContextWithFetch(func(oid base.OutputID) ([]byte, bool) {
			return pred.Output.Bytes(), oid == pred.ID
		}))
		require.NoError(t, vtx.ValidateFullContext())
		// through the input queue, which posts it to the sequencers' mine registries as a node
		// does; the test environment has no own sequencer, so the queue's attach gate drops it as
		// an access node would, and it is attached directly a few ticks later
		testData.wrk.TxBytesInFromAPIQueued(txBytes)
		time.Sleep(2 * ledger.TickDuration())
		txid, err := testData.wrk.TxBytesInForTests(txBytes)
		require.NoError(t, err)
		transits = append(transits, submitted{txid, versions[target]})
		t.Logf("transit %s on %s -> %s", txid.StringShort(), pred.ID.StringShort(), versions[target])
	}
	waitUntil(base.T(resumeSlot+miningSlots+4, 0))
	inRegistry := map[string]int{}
	for i, tr := range transits {
		reg := testData.sequencers[i%len(versions)].(interface{ MineTransits() *mine.Registry }).MineTransits()
		if _, ok := reg.Validated(tr.txid); ok {
			inRegistry[tr.version]++
		}
	}
	t.Logf("transits validated in their target's registry, by version: %v", inRegistry)
	require.True(t, testData.stopAndWait(10*time.Second))

	store := testData.env.StateStore()
	upgradeOID := base.UpgradeOutputID(upgradeSlot)
	branchesAfter := 0
	for slot := uint32(upgradeSlot); slot <= multistate.FetchLatestCommittedSlot(store); slot++ {
		for _, rr := range multistate.FetchRootRecords(store, slot) {
			rdr, err := multistate.NewReadable(store, rr.Root)
			require.NoError(t, err)
			require.True(t, rdr.HasUTXO(upgradeOID), "a branch of slot %d lacks the upgrade UTXO", slot)
			branchesAfter++
		}
	}
	t.Logf("branches committed at or after the upgrade slot %d: %d", upgradeSlot, branchesAfter)
	require.Greater(t, branchesAfter, pauseSlots+miningSlots/2, "branches must keep coming after the upgrade slot")

	msMutex.Lock()
	bad := 0
	for _, ms := range milestones {
		if ms.GetTxStatus() == vertex.Bad {
			bad++
			t.Logf("BAD milestone %s: %v", ms.IDShortString(), ms.GetError())
		}
	}
	t.Logf("milestones observed: %d, BAD: %d", len(milestones), bad)
	msMutex.Unlock()
	require.Zero(t, bad, "no milestone may turn BAD")

	lrb := multistate.FindLatestReliableBranch(store)
	require.NotNil(t, lrb)
	final := multistate.MustNewSugaredReadableState(store, lrb.Root)
	require.True(t, final.KnowsCommittedTransaction(transferBefore), "the transfer before the slot is not confirmed")
	require.True(t, final.KnowsCommittedTransaction(transferAfter), "the transfer after the slot is not confirmed")

	finalMine, err := final.GetChainOutputWithID(base.MineChainID)
	require.NoError(t, err)
	advanced := finalMine.Output.ChainConstraint().TransitionCounter - initialMine.Output.ChainConstraint().TransitionCounter
	byVersion := map[string]int{}
	for _, tr := range transits {
		// the reward tag-along is output 1: only its target can take it this early, so a
		// committed transit whose tag-along is spent was included by that sequencer
		_, unspent := final.GetUTXO(base.MustNewOutputID(tr.txid, 1))
		if final.KnowsCommittedTransaction(tr.txid) && !unspent {
			byVersion[tr.version]++
		}
	}
	t.Logf("mine chain advanced %d transits in the LRB (slot %d); included by version: %v", advanced, lrb.Stem.ID.Slot(), byVersion)
	require.Greater(t, int(advanced), miningSlots/2, "the mine chain must advance")
	for _, v := range versions {
		require.Positive(t, byVersion[v], "no transit was included by a %s sequencer", v)
	}
}

// buildTestHashTransit is a transaction-hash transit of the mine output pred stamped at ts,
// signed by priv, its reward A one tag-along to target, the nonce in TxConstraints[0] searched
// until the ID meets K.
func buildTestHashTransit(t *testing.T, pred *ledger.OutputWithID, priv ed25519.PrivateKey, ts base.LedgerTime, target base.ChainID) []byte {
	t.Helper()
	lib := ledger.L(ts.Slot)
	lockBin, err := pred.Output.At(int(ledger.ConstraintIndexLock))
	require.NoError(t, err)
	predLock, err := ledger.MineLockFromBytesWithLib(lockBin, lib)
	require.NoError(t, err)
	cc := pred.Output.ChainConstraint()
	predSlot := pred.ID.Timestamp().Slot
	a := lib.MineAmountAtSlot(ts.Slot)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(pred.Output, pred.ID)
	require.NoError(t, err)
	succB, succC := lib.MineRetarget(predLock.B, predLock.C, predSlot, ts.Slot)
	succ := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(pred.Output.TokenBalance()), int64(a))
		o.PutConstraint(ledger.NewMineLock(predLock.R-a, succB, succC, uint64(lib.MinerVersion), 1).Bytes(), ledger.ConstraintIndexLock)
		o.PutConstraint(ledger.NewChainConstraint(base.MineChainID, predIdx, cc.OriginSlot,
			cc.CumulativeChainInflation+a, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(succ)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewTagAlongOutput(a, target, base.HolderIDFromED25519PrivateKey(priv)))
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()

	k := int(lib.MineRequiredK(predLock.B, uint64(ts.Slot-predSlot)))
	for n := uint64(0); ; n++ {
		var nonce [8]byte
		binary.BigEndian.PutUint64(nonce[:], n)
		txb.TxData.TxConstraints = [][]byte{easyfl.InlineDataBytecode(nonce[:])}
		txb.SignED25519(priv)
		id, err := txbuildercore.TxIDFromBytes(txb.Bytes())
		require.NoError(t, err)
		if bits.TrailingZeros64(binary.BigEndian.Uint64(id[24:])) >= k {
			break
		}
	}
	tx, err := transaction.Parse(txb.Bytes())
	require.NoError(t, err)
	require.True(t, tx.IsMiningTransaction() && tx.MineWorkIsTxID())
	return txb.Bytes()
}
