package tests

// Safeguard test: a mine transition under the upgrade 6 rules mints exactly A. The existing test
// (TestUpgrade6HashTransit) covers second input, work, payout, signature and timing, not the core
// minting rules: successor inflation cell = A and R' = R - A (_mineSuccessorState,
// upgrade6.easyfl). Built like buildHashTransit, with two knobs: minted amount (inflation cell and
// payout) and R on the successor. Cumulative chain inflation and payout are adjusted to match, so
// only the mine rule can refuse. Needs mineConst/withUpgrade6/workMeets/constAt from the upgrade 6 tests.

import (
	"crypto/ed25519"
	"encoding/binary"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// mineTransitMinting builds a transition minting `minted` (successor inflation cell, cumulative chain
// inflation, tag-along payout) with succR on the successor, and searches a solution of the work.
func mineTransitMinting(t *testing.T, u *utxodb.UTXODB, priv ed25519.PrivateKey, slot uint32, minted, succR uint64) []byte {
	t.Helper()
	lib := ledger.L(slot)
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	mineIn, err := md.Parse()
	require.NoError(t, err)
	lockBin, err := mineIn.Output.At(int(ledger.ConstraintIndexLock))
	require.NoError(t, err)
	predLock, err := ledger.MineLockFromBytesWithLib(lockBin, lib)
	require.NoError(t, err)
	cc := mineIn.Output.ChainConstraint()
	predSlot := mineIn.ID.Timestamp().Slot
	signer := ledger.SigLockFromED25519PrivateKey(priv)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(mineIn.Output, mineIn.ID)
	require.NoError(t, err)
	succB, succC := lib.MineRetarget(predLock.B, predLock.C, predSlot, slot)
	succ := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(mineIn.Output.TokenBalance()), int64(minted))
		o.PutConstraint(ledger.NewMineLock(succR, succB, succC, constAt(t, slot, "constMinerVersion"), 1).Bytes(), ledger.ConstraintIndexLock)
		o.PutConstraint(ledger.NewChainConstraint(base.MineChainID, predIdx, cc.OriginSlot,
			cc.CumulativeChainInflation+minted, 0, cc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(succ)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewTagAlongOutput(minted, *u.GenesisChainID(), base.HolderID(signer)))
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	txb.SetTimestamp(base.T(slot, 90))
	txb.ComputeInputCommitment()

	k := int(lib.MineRequiredK(predLock.B, uint64(slot-predSlot)))
	for n := uint64(0); ; n++ {
		var nonce [8]byte
		binary.BigEndian.PutUint64(nonce[:], n)
		txb.TxData.TxConstraints = [][]byte{easyfl.InlineDataBytecode(nonce[:])}
		txb.SignED25519(priv)
		id, err := txbuildercore.TxIDFromBytes(txb.Bytes())
		require.NoError(t, err)
		if workMeets(id, k) {
			break
		}
	}
	return txb.Bytes()
}

func TestMineTransitMintsExactlyA(t *testing.T) {
	slot := uint32(upgrade6Slot + 5)
	a := mineConst(t, "constMineAmountBase")
	newEnv := func(t *testing.T) (*utxodb.UTXODB, ed25519.PrivateKey, uint64) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		withUpgrade6(t, u)
		priv, _, addr := u.GenerateAddress(3)
		require.NoError(t, u.TokensFromFaucet(addr, 100*base.PROX))
		md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
		require.NoError(t, err)
		mineIn, err := md.Parse()
		require.NoError(t, err)
		lockBin, err := mineIn.Output.At(int(ledger.ConstraintIndexLock))
		require.NoError(t, err)
		predLock, err := ledger.MineLockFromBytesWithLib(lockBin, ledger.L(slot))
		require.NoError(t, err)
		return u, priv, predLock.R
	}

	t.Run("reference: mint A, lower R by A", func(t *testing.T) {
		u, priv, r := newEnv(t)
		require.NoError(t, u.AddTransaction(mineTransitMinting(t, u, priv, slot, a, r-a)))
	})
	t.Run("mint more than A (R lowered to match)", func(t *testing.T) {
		u, priv, r := newEnv(t)
		err := u.AddTransaction(mineTransitMinting(t, u, priv, slot, a+1, r-a-1))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "mine successor inflation must equal the minted amount"), "%v", err)
	})
	t.Run("mint A, R not lowered", func(t *testing.T) {
		u, priv, r := newEnv(t)
		err := u.AddTransaction(mineTransitMinting(t, u, priv, slot, a, r))
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "wrong R on mine successor"), "%v", err)
	})
}
