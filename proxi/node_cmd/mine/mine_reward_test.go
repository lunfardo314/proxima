package mine

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// The payout and the reward watch under transaction-hash work (kb/library_upgrade6_mining.md).

// withSequencers gives the miner a fresh list of active sequencers for slot, so the
// payout choice reads no node.
func withSequencers(f *verifyFixture, slot uint32, cands ...txbuildercore.SequencerCandidate) {
	f.m.seqs = make(map[base.ChainID]txbuildercore.SequencerCandidate)
	for _, c := range cands {
		f.m.seqs[c.ID] = c
	}
	f.m.seqsSlot, f.m.seqFetchSlots = slot, maxSeqFetchSlots
	f.m.stalledTargets = make(map[base.ChainID]uint32)
}

// With mine.payout_address the reward is a sigLock of A less the inclusion fee to that
// holder, and the fee, the larger of the profile's and the target's minimum, a tag-along
// to the target; the miner's own search and verification accept the transit.
func TestMinePayoutAddress(t *testing.T) {
	f := hashFixture(t)
	succSlot, succB, succC := f.honestTarget()
	const minFee = 5_000
	withSequencers(f, succSlot, txbuildercore.SequencerCandidate{ID: f.m.tagAlongSeqID, MinimumFee: minFee})
	_, addrKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	addr := base.HolderIDFromED25519PrivateKey(addrKey)
	f.m.payoutAddress = &addr

	p, ok := f.m.choosePayout(succSlot)
	require.True(t, ok)
	a := f.m.consts.MineAmountAtSlot(succSlot)
	require.Equal(t, a, p.minted, "every transit mints A")
	require.EqualValues(t, minFee, p.fee)
	require.Equal(t, addr, *p.holder)

	txb, _ := f.m.buildTransit(f.pred, succSlot, succB, succC, p)
	ht, err := newHashTemplate(txb, f.m.holderID)
	require.NoError(t, err)
	k := int(f.m.consts.MineRequiredK(f.pred.ml.B, uint64(succSlot-f.pred.oid.Timestamp().Slot)))
	var sol *mineSolution
	f.m.mineParallel(f.pred.oid, succSlot, k, time.Now().Add(30*time.Second), func() []byte { return nil },
		func(s mineSolution) { sol = &s; f.m.abort.Store(true) }, ht)
	require.NotNil(t, sol)
	txb, _ = f.m.buildTransit(f.pred, succSlot, succB, succC, p)
	txb.TxData.TxConstraints = [][]byte{mineNonceBytecode(sol.nonce)}
	txb.SignED25519(f.m.wallet.PrivateKey)
	_, err = verifyMineTransit(f.m.lib, f.m.constsAt, f.pred, txb.Bytes())
	require.NoError(t, err)

	tx, err := transaction.ParseLibraryAgnostic(txb.Bytes())
	require.NoError(t, err)
	reward, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(1))
	require.NoError(t, err)
	fee, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(2))
	require.NoError(t, err)
	require.Equal(t, a-minFee, reward)
	require.EqualValues(t, minFee, fee)
	kind, err := f.m.lib.ClassifyLock(tx.MustOutputDataAt(1), addr)
	require.NoError(t, err)
	require.EqualValues(t, txbuildercore.LockKindSig, kind, "the reward is the payout address's")
}

// The reward of a confirmed own transit is watched: a target that leaves it for
// rewardTakeSlots is left out of the choice, and a reward consumed is forgotten. Only a
// reward that is a tag-along is watched.
func TestMineRewardWatch(t *testing.T) {
	f := hashFixture(t)
	var target base.ChainID
	target[0] = 0x77
	var txid base.TransactionID
	txid[0] = 1
	oid := base.MustNewOutputID(txid, 1)
	present := true
	f.m.outputData = func(id *base.OutputID) ([]byte, error) {
		if *id == oid && present {
			return []byte{1}, nil
		}
		return nil, nil
	}
	f.m.stalledTargets = make(map[base.ChainID]uint32)

	f.m.watchReward(txid, &minePayout{option: txbuildercore.MinePayoutDelegation, target: target}, 100)
	require.Empty(t, f.m.rewards, "a new delegation is not a tag-along reward")
	f.m.watchReward(txid, &minePayout{option: txbuildercore.MinePayoutFull, target: target, topUp: true}, 100)
	require.Len(t, f.m.rewards, 1)

	f.m.checkRewardsAt(101)
	require.NotContains(t, f.m.stalledTargets, target, "the target still has time")
	f.m.checkRewardsAt(100 + rewardTakeSlots)
	require.Greater(t, f.m.stalledTargets[target], uint32(100+rewardTakeSlots), "a target that leaves the reward is left out")

	present = false
	f.m.checkRewardsAt(100 + rewardTakeSlots + 1)
	require.Empty(t, f.m.rewards, "a consumed reward is forgotten")
}

// The reclaim of a reward tag-along returns the reward to the miner less an inclusion fee,
// paid as a tag-along to the fee target.
func TestMineRewardReclaim(t *testing.T) {
	f := hashFixture(t)
	const minFee = 2_000
	withSequencers(f, 200, txbuildercore.SequencerCandidate{ID: f.m.tagAlongSeqID, MinimumFee: minFee})
	a := f.m.consts.MineAmountAtSlot(100)
	rewardOut, err := txbuildercore.NewTagAlongOutput(f.m.lib, a, f.m.tagAlongSeqID, f.m.holderID)
	require.NoError(t, err)
	var txid base.TransactionID
	copy(txid[:], base.T(100, 90).Bytes())
	txid[base.LedgerTimeByteLength] = 1 // the transit has two outputs
	oid := base.MustNewOutputID(txid, 1)

	txBytes, ok := f.m.buildReclaim(oid, rewardOut.Bytes(), base.T(200, 10))
	require.True(t, ok)
	tx, err := transaction.ParseLibraryAgnostic(txBytes)
	require.NoError(t, err)
	require.Equal(t, oid, tx.MustInputAt(0))
	back, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(0))
	require.NoError(t, err)
	fee, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(1))
	require.NoError(t, err)
	require.Equal(t, a-minFee, back)
	require.EqualValues(t, minFee, fee)
	_, err = verifyTxSignature(tx)
	require.NoError(t, err)
}
