package tests

import (
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/stretchr/testify/require"
)

// TODO must be two different controllers, target and delegator should be different

func TestTagAlongSimple(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)
	var u *utxodb.UTXODB
	var privKeySender, privKeyTarget, privKeyRandom ed25519.PrivateKey
	var addrSender, addrTarget, addrRandom ledger.AddressED25519
	var initOutputID base.OutputID
	var seqOrigin *ledger.OutputWithChainID
	var err error

	var targetChainID base.ChainID

	// creates chain and tag-along output
	initTest := func(prntx bool) {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 3, initAmount)
		privKeySender = privKeys[0]
		addrSender = addrs[0]
		privKeyTarget = privKeys[1]
		addrTarget = addrs[1]
		privKeyRandom = privKeys[2]
		addrRandom = addrs[2]
		t.Logf("sender address: %s\n", addrSender.String())
		t.Logf("target address: %s\n", addrTarget.String())
		t.Logf("random address: %s\n", addrRandom.String())

		// create chain
		seqOrigin, err = u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, ledger.TimeNow().AddSlots(1))
		require.NoError(t, err)
		targetChainID = seqOrigin.ChainID

		// sender creates tx with tag-along to the target chain
		txb := txbuilder.New()

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.AccountID())
		require.NoError(t, err)
		require.True(t, len(outs) > 0)

		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		o := ledger.NewTagAlongOutput(fee, targetChainID, privKeySender)
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		ts := seqOrigin.ID.Timestamp().AddSlots(1)
		txb.TransactionData.Timestamp = ts.AddSlots(1)
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKeySender)

		txBytes, txid, txString, err := txb.BytesWithValidation()
		if prntx {
			t.Logf("------------- tag-along tx --------------\n%s", txString)
		}
		require.NoError(t, err)

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		initOutputID = base.MustNewOutputID(txid, 0)
	}
	getTagAlongTs := func() base.LedgerTime {
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))
		return taOuts[0].ID.Timestamp()
	}

	transitTxWithTagAlong := func(ts base.LedgerTime, prntx bool) ([]byte, error) {
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb := txbuilder.New()
		_, err = txb.ConsumeOutput(seqOrigin.Output, seqOrigin.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))

		// transit chain
		next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(seqOrigin.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
			o.WithLock(seqOrigin.Output.Lock())
			cc := ledger.NewChainConstraint(targetChainID, 0, 2, seqOrigin.OriginSlot, seqOrigin.OriginAmount)
			o.MustPushConstraint(cc.Bytes())
		})
		_, err = txb.ProduceOutput(next)
		require.NoError(t, err)

		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKeyTarget)
		txBytes, _, txString, err := txb.BytesWithValidation()
		if prntx {
			t.Logf("----------------- transit tx ------------------\n%s", txString)
		}
		if err != nil {
			return nil, err
		}
		return txBytes, nil
	}
	reclaimTagAlong := func(ts base.LedgerTime, reclaimerPrivateKey ed25519.PrivateKey, prntx bool) error {
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb := txbuilder.New()
		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		reclaimerAddr := ledger.AddressED25519FromPrivateKey(reclaimerPrivateKey)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(taOuts[0].Output.TokenBalance())
			o.WithLock(reclaimerAddr)
		}))

		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(reclaimerPrivateKey)
		_, _, txString, err := txb.BytesWithValidation()
		if prntx {
			t.Logf("------------- reclaim tx --------------\n%s", txString)
		}
		return err
	}

	t.Run("init", func(t *testing.T) {
		initTest(false)
		outsMaster, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.AccountID())
		require.NoError(t, err)
		require.True(t, len(outsMaster) == 2)

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
	t.Run("iterate", func(t *testing.T) {
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
	t.Run("consume until reclaim window", func(t *testing.T) {
		initTest(false)
		taTs := getTagAlongTs()
		for i := uint32(1); ; i++ {
			ts := taTs.AddSlots(i)
			_, err = transitTxWithTagAlong(ts, false)
			if err == nil {
				t.Logf("%d   taTs: %s, txTs %s OK", i, taTs.String(), ts.String())
			} else {
				t.Logf("%d   taTs: %s, txTs %s FAILED with error '%v'", i, taTs.String(), ts.String(), err)
				require.NoError(t, util.MustErrorWith(err, "inside reclaim slots must be unlocked by the sender"))
				break
			}
		}
	})
	t.Run("consume with reclaim by sender", func(t *testing.T) {
		initTest(false)
		taTs := getTagAlongTs()
		for i := uint32(1); ; i++ {
			ts := taTs.AddSlots(i)
			err = reclaimTagAlong(ts, privKeySender, false)
			if err == nil {
				t.Logf("%d   taTs: %s, txTs %s reclaim OK", i, taTs.String(), ts.String())
				ts = ts.AddSlots(10)
				err = reclaimTagAlong(ts, privKeySender, false)
				require.NoError(t, err)
				t.Logf("%d   taTs: %s, txTs %s reclaim OK", i, taTs.String(), ts.String())
				break
			}
			//t.Logf("%d   taTs: %s, txTs %s FAILED with error '%v'", i, taTs.String(), ts.String(), err)
			require.NoError(t, util.MustErrorWith(err, "inside tag along slots must be unlocked by the target"))
		}
	})
	t.Run("consume with reclaim by random", func(t *testing.T) {
		initTest(false)
		taTs := getTagAlongTs()
		for i := uint32(1); ; i++ {
			ts := taTs.AddSlots(i)
			err = reclaimTagAlong(ts, privKeyRandom, false)
			if err == nil {
				t.Logf("%d   taTs: %s, txTs %s reclaim OK", i, taTs.String(), ts.String())
				ts = ts.AddSlots(10)
				err = reclaimTagAlong(ts, privKeySender, false)
				require.NoError(t, err)
				t.Logf("%d   taTs: %s, txTs %s reclaim OK", i, taTs.String(), ts.String())
				break
			}
			require.True(t, ts.Slot < taTs.Slot+ledger.Const.TagAlongReclaimSlots)
			//t.Logf("%d   taTs: %s, txTs %s FAILED with error '%v'", i, taTs.String(), ts.String(), err)
			require.NoError(t, util.MustErrorWith(err, "unlock window error"))
		}
	})
}
