package monitor

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util/vrf"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// buildTransit consumes the current mine chain output of u and builds the next
// valid transit at the minimum pace, signed by and paid out to miner: the
// successor mine output at index 0, the payout at 1 and the tag-along fee at
// 2, with a VRF proof meeting the required K. A reduced copy of the builder in
// the ledger mine tests, enough to populate a txstore for the back-walk.
func buildTransit(t *testing.T, u *utxodb.UTXODB, miner ed25519.PrivateKey, fee uint64) []byte {
	t.Helper()
	lib := ledger.L(0)
	a := lib.Constants.MineAmountBase

	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)
	lockBin, err := mineIn.Output.At(int(ledger.ConstraintIndexLock))
	require.NoError(t, err)
	predLock, err := ledger.MineLockFromBytesWithLib(lockBin, lib)
	require.NoError(t, err)
	cc := mineIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	predSlot := mineIn.ID.Timestamp().Slot
	succSlot := predSlot + uint32(lib.Constants.MineMinPace)
	minerLock := ledger.SigLockFromED25519PrivateKey(miner)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(mineIn.Output, mineIn.ID)
	require.NoError(t, err)

	succB, succC := lib.MineRetarget(predLock.B, predLock.C, predSlot, succSlot)
	succ := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(mineIn.Output.TokenBalance()), int64(a)).
			WithLock(ledger.NewMineLock(predLock.R-a, succB, succC))
		o.PutConstraint(ledger.NewChainConstraint(base.MineChainID, predIdx, cc.OriginSlot,
			cc.CumulativeChainInflation+a, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(a - fee).WithLock(minerLock)
	}))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewTagAlongOutput(fee, *u.GenesisChainID(), base.HolderID(minerLock)))
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.SetTimestamp(base.T(succSlot, 1))
	txb.ComputeInputCommitment()

	k := int(lib.MineRequiredK(predLock.B, uint64(lib.Constants.MineMinPace)))
	prover, err := vrf.NewProver(miner)
	require.NoError(t, err)
	var nonce [txbuildercore.MineNonceLen]byte
	for n := uint64(0); ; n++ {
		binary.BigEndian.PutUint64(nonce[:], n)
		beta, st, err := prover.Output(txbuildercore.MineVRFMessage(mineIn.ID, succSlot, nonce))
		require.NoError(t, err)
		if trailingZeroBits(beta) >= k {
			pi, err := prover.ProofFor(st)
			require.NoError(t, err)
			txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, txbuildercore.MineUnlockParams(pi, nonce))
			break
		}
	}
	txb.SignED25519(miner)
	return txb.Bytes()
}

// TestMineHistoryWalk mines five transits from two miners into a utxodb and a
// txstore, then checks the back-walk: the difficulty series covers every
// transit in the chart window newest first, rewards are summed per holder ID
// from the payout outputs, and the walk stops when the store runs out.
// A second walk with the LRB placed beyond the chart window checks that the
// series and the reward table are windowed while the transit list is not.
func TestMineHistoryWalk(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	store := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	_, minerA, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, minerB, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	a := ledger.L(0).Constants.MineAmountBase
	fee := ledger.L(0).Constants.MineTagAlongFee
	miners := []ed25519.PrivateKey{minerA, minerB, minerA, minerB, minerA}
	var lastSlot uint32
	for _, priv := range miners {
		txBytes := buildTransit(t, u, priv, fee)
		require.NoError(t, u.AddTransaction(txBytes))
		txid, err := store.PersistTxBytes(txBytes)
		require.NoError(t, err)
		lastSlot = txid.Slot()
	}
	hA := base.HolderIDFromED25519PrivateKey(minerA)
	hB := base.HolderIDFromED25519PrivateKey(minerB)
	holderA, holderB := hex.EncodeToString(hA[:]), hex.EncodeToString(hB[:])

	env := &testEnv{u: u, store: store, lrbSlot: lastSlot}
	hist := (&Monitor{env: env}).collectMineHistory()
	require.NotNil(t, hist)
	// the test store holds the transits only, so the walk ends where the
	// genesis mine output's transaction would be
	require.Equal(t, "txstore does not reach further back", hist.TruncatedBy)
	require.Equal(t, len(miners), hist.Depth)
	require.Equal(t, len(miners), hist.MinedLastHour)

	// series: one sample per transit, newest first, carrying that transit's B
	require.Len(t, hist.Series, len(miners))
	for i, s := range hist.Series {
		require.Equal(t, hist.Transits[i].Slot, s.Slot)
		require.Equal(t, hist.Transits[i].Difficulty, s.Difficulty)
		if i > 0 {
			require.Less(t, s.Slot, hist.Series[i-1].Slot)
		}
	}
	// rewards: the payout amount (mint less fee) summed per holder, biggest first
	require.Equal(t, 2, hist.NumMiners)
	require.Equal(t, []minerRow{
		{Holder: holderA, Transits: 3, Amount: 3 * (a - fee)},
		{Holder: holderB, Transits: 2, Amount: 2 * (a - fee)},
	}, hist.Miners)
	require.Equal(t, holderA, hist.Transits[0].Miner)

	// LRB far beyond the chart window: nothing recent, but the transit list is
	// still reported so the pace and the table have something to show
	env.lrbSlot = lastSlot + uint32(hist.ChartWindowSlots) + 1
	hist = (&Monitor{env: env}).collectMineHistory()
	require.NotNil(t, hist)
	require.Equal(t, len(miners), hist.Depth)
	require.Empty(t, hist.Series)
	require.Empty(t, hist.Miners)
	require.Equal(t, 0, hist.NumMiners)
	require.Equal(t, 0, hist.MinedLastHour)
}
