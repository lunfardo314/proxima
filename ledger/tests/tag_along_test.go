package tests

import (
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/stretchr/testify/require"
)

func TestTagAlongSimple(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)
	var u *utxodb.UTXODB
	var privKey ed25519.PrivateKey
	var addr ledger.AddressED25519
	var initOutputID base.OutputID
	var seqOut *ledger.OutputWithChainID
	var err error

	var targetChainID base.ChainID

	initTest := func(prntx bool) {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 1, initAmount)
		privKey = privKeys[0]
		addr = addrs[0]

		seqOut, err = u.MakeNewChain(chainAmount, privKey, addr, ledger.TimeNow().AddSlots(1))
		require.NoError(t, err)
		targetChainID = seqOut.ChainID

		txb := txbuilder.New()

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.AccountID())
		require.NoError(t, err)
		require.True(t, len(outs) > 0)

		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		o := ledger.NewTagAlongOutput(fee, targetChainID, privKey)
		t.Logf("\ntarget ID: %s\naddr: %s, tag-along output:\n%s", targetChainID.String(), addr.String(), o.String())
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addr)
		}))
		require.NoError(t, err)

		ts := seqOut.ID.Timestamp().AddSlots(1)
		txb.TransactionData.Timestamp = ts.AddSlots(1)
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKey)

		txBytes, txid, txString, err := txb.BytesWithValidation()
		if prntx {
			t.Logf("\n%s", txString)
		}
		require.NoError(t, err)

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		initOutputID = base.MustNewOutputID(txid, 0)
	}

	t.Run("1", func(t *testing.T) {
		initTest(false)
		outsMaster, err := u.SugaredStateReader().GetOutputsForAccount(addr.AccountID())
		require.NoError(t, err)
		require.True(t, len(outsMaster) == 3)

		outsTarget, err := u.SugaredStateReader().GetOutputsForAccount(ledger.ChainLockFromChainID(targetChainID).AccountID())
		require.NoError(t, err)
		require.True(t, len(outsTarget) == 1)

		idxMaster := slices.IndexFunc(outsMaster, func(o *ledger.OutputWithID) bool {
			return o.ID == initOutputID
		})
		require.True(t, idxMaster >= 0)
		idxTarget := slices.IndexFunc(outsTarget, func(o *ledger.OutputWithID) bool {
			return o.ID == initOutputID
		})
		require.True(t, idxTarget >= 0)
	})
	t.Run("2", func(t *testing.T) {
		initTest(false)
		t.Logf("iterate tag along account for %s", targetChainID.String())
		count := 0
		ts := ledger.TimeNow().AddSlots(1)
		err := u.SugaredStateReader().IterateTagAlongBacklog(targetChainID, func(o *ledger.TagAlongOutput) bool {
			ln := lines.New("         ")
			for _, slot := range []uint32{
				ts.Slot,
				ts.Slot + ledger.Const.TagAlongSlots - 1,
				ts.Slot + ledger.Const.TagAlongSlots,
				ts.Slot + ledger.Const.TagAlongSlots + 1,
				ts.Slot + ledger.Const.TagAlongReclaimSlots - 1,
				ts.Slot + ledger.Const.TagAlongReclaimSlots,
				ts.Slot + ledger.Const.TagAlongReclaimSlots + 1,
				ts.Slot + ledger.Const.TagAlongReclaimSlots + 1000,
			} {
				ln.Add("      status in slot %d: %s", slot, o.StatusInSlot(slot))
			}
			t.Logf("\n%d  %s\n%s\n%s", count, o.ID.String(), o.Output.LinesHR("      "), ln.String())

			count++
			return true
		})
		require.NoError(t, err)
	})
	t.Run("3", func(t *testing.T) {
		initTest(false)
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb := txbuilder.New()
		_, err = txb.ConsumeOutput(seqOut.Output, seqOut.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))

		next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(seqOut.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
			o.WithLock(seqOut.Output.Lock())
			cc := ledger.NewChainConstraint(targetChainID, 0, 2, seqOut.OriginSlot, seqOut.OriginAmount)
			o.MustPushConstraint(cc.Bytes())
		})
		_, err = txb.ProduceOutput(next)
		require.NoError(t, err)

		txb.TransactionData.Timestamp = base.MaximumTime(taOuts[0].ID.Timestamp(), seqOut.ID.Timestamp()).AddSlots(1)
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKey)
		txBytes, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n%s", txString)
		require.NoError(t, err)

		u.AddTransaction(txBytes)
	})
}
