package tests

import (
	"crypto/ed25519"
	"slices"
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
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
	var privKeySender, privKeyTarget, privKeyRandom ed25519.PrivateKey
	var addrSender, addrTarget, addrRandom ledger.SigLock
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

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err = u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID = seqOrigin.ChainID

		// sender creates tx with tag-along to the target chain
		txb := txbuilder.New()

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		require.True(t, len(outs) > 0)

		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		o := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		ts := seqOrigin.ID.Timestamp().AddSlots(1)
		txb.SetTimestamp(ts.AddSlots(1))
		txb.ComputeInputCommitment()
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
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

		// transit chain
		next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(seqOrigin.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
			o.WithLock(seqOrigin.Output.Lock())
			cc := ledger.NewChainConstraint(targetChainID, 0, seqOrigin.OriginSlot, 0, 0, seqOrigin.TransitionCounter+1, 0)
			o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		})
		_, err = txb.ProduceOutput(next)
		require.NoError(t, err)

		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
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

		// Important: must consolidate small amount into the bigger one, otherwise cannot consume

		reclaimerAddr := ledger.SigLockFromED25519PrivateKey(reclaimerPrivateKey)
		outs, err := u.SugaredStateReader().GetOutputsForAccount(reclaimerAddr.ControllerID())
		if err != nil {
			return err
		}
		outs = util.PurgeSlice(outs, func(o *ledger.OutputWithID) bool {
			return o.Output.Lock().Name() == ledger.SigLockName
		})
		require.True(t, len(outs) > 0)
		maxOut := slices.MaxFunc(outs, func(a, b *ledger.OutputWithID) int {
			b1 := a.Output.TokenBalance()
			b2 := b.Output.TokenBalance()
			if b1 == b2 {
				return 0
			}
			if b1 < b2 {
				return -1
			}
			return 1
		})
		idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
		require.NoError(t, err)

		err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
			o.WithLock(reclaimerAddr)
		}))
		if err != nil {
			return err
		}
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(reclaimerPrivateKey)
		_, _, txString, err := txb.BytesWithValidation()
		if prntx {
			t.Logf("------------- reclaim tx --------------\n%s", txString)
		}
		return err
	}

	t.Run("init", func(t *testing.T) {
		initTest(false)
		outsMaster, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		require.True(t, len(outsMaster) == 2)

		outsTarget, err := u.SugaredStateReader().GetOutputsForAccount(ledger.ChainLockFromChainID(targetChainID).ControllerID())
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
				ts.Slot + ledger.L(0).TagAlongSlots - 1,
				ts.Slot + ledger.L(0).TagAlongSlots,
				ts.Slot + ledger.L(0).TagAlongSlots + 1,
				ts.Slot + ledger.L(0).TagAlongReclaimSlots - 1,
				ts.Slot + ledger.L(0).TagAlongReclaimSlots,
				ts.Slot + ledger.L(0).TagAlongReclaimSlots + 1,
				ts.Slot + ledger.L(0).TagAlongReclaimSlots + 1000,
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
			require.True(t, ts.Slot < taTs.Slot+ledger.L(0).TagAlongReclaimSlots)
			//t.Logf("%d   taTs: %s, txTs %s FAILED with error '%v'", i, taTs.String(), ts.String(), err)
			require.NoError(t, util.MustErrorWith(err, "unlock window error"))
		}
	})
}

// TestTagAlongBoundaries tests exact slot boundary transitions for tag-along unlock windows.
// It verifies that:
// - At slot TagAlongSlots-1, target can still unlock (tag-along window)
// - At slot TagAlongSlots, target cannot unlock (entered reclaim window)
// - At slot TagAlongReclaimSlots-1, random cannot unlock (still reclaim window)
// - At slot TagAlongReclaimSlots, random can unlock (entered purge window)
func TestTagAlongBoundaries(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)

	t.Run("exact boundary at TagAlongSlots", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		// create tag-along output
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		taTs := seqOrigin.ID.Timestamp().AddSlots(2)
		txb.SetTimestamp(taTs)
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// helper to try target unlock at specific slot offset
		tryTargetUnlock := func(slotOffset uint32) error {
			taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
			if len(taOuts) != 1 {
				return nil
			}

			txb := txbuilder.New()
			_, err = txb.ConsumeOutput(seqOrigin.Output, seqOrigin.ID)
			require.NoError(t, err)
			txb.PutSignatureUnlock(0)
			txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

			_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
			require.NoError(t, err)
			txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

			next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(seqOrigin.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
				o.WithLock(seqOrigin.Output.Lock())
				cc := ledger.NewChainConstraint(targetChainID, 0, seqOrigin.OriginSlot, 0, 0, seqOrigin.TransitionCounter+1, 0)
				o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
			})
			_, err = txb.ProduceOutput(next)
			require.NoError(t, err)

			txb.SetTimestamp(taTs.AddSlots(slotOffset))
			txb.ComputeInputCommitment()
			txb.SignED25519(privKeyTarget)
			_, _, _, err := txb.BytesWithValidation()
			return err
		}

		// slot TagAlongSlots-1 should succeed (still in tag-along window)
		err = tryTargetUnlock(ledger.L(0).TagAlongSlots - 1)
		require.NoError(t, err, "target should unlock at slot TagAlongSlots-1")

		// slot TagAlongSlots should fail (entered reclaim window)
		err = tryTargetUnlock(ledger.L(0).TagAlongSlots)
		require.Error(t, err, "target should NOT unlock at slot TagAlongSlots")
		require.True(t, strings.Contains(err.Error(), "inside reclaim slots must be unlocked by the sender"))
	})

	t.Run("exact boundary at TagAlongReclaimSlots", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 3, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]
		privKeyRandom := privKeys[2]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		// create tag-along output
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		taTs := seqOrigin.ID.Timestamp().AddSlots(2)
		txb.SetTimestamp(taTs)
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// helper to try random unlock at specific slot offset
		tryRandomUnlock := func(slotOffset uint32) error {
			taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
			if len(taOuts) != 1 {
				return nil
			}

			reclaimerAddr := ledger.SigLockFromED25519PrivateKey(privKeyRandom)
			reclaimerOuts, err := u.SugaredStateReader().GetOutputsForAccount(reclaimerAddr.ControllerID())
			require.NoError(t, err)
			reclaimerOuts = util.PurgeSlice(reclaimerOuts, func(o *ledger.OutputWithID) bool {
				return o.Output.Lock().Name() == ledger.SigLockName
			})
			require.True(t, len(reclaimerOuts) > 0)

			txb := txbuilder.New()
			_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
			require.NoError(t, err)
			txb.PutSignatureUnlock(0)

			maxOut := slices.MaxFunc(reclaimerOuts, func(a, b *ledger.OutputWithID) int {
				if a.Output.TokenBalance() < b.Output.TokenBalance() {
					return -1
				}
				if a.Output.TokenBalance() > b.Output.TokenBalance() {
					return 1
				}
				return 0
			})
			idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
			require.NoError(t, err)
			err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
			require.NoError(t, err)

			_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
				o.WithLock(reclaimerAddr)
			}))
			require.NoError(t, err)

			txb.SetTimestamp(taTs.AddSlots(slotOffset))
			txb.ComputeInputCommitment()
			txb.SignED25519(privKeyRandom)
			_, _, _, err = txb.BytesWithValidation()
			return err
		}

		// slot TagAlongReclaimSlots-1 should fail (still in reclaim window, only sender can unlock)
		err = tryRandomUnlock(ledger.L(0).TagAlongReclaimSlots - 1)
		require.Error(t, err, "random should NOT unlock at slot TagAlongReclaimSlots-1")

		// slot TagAlongReclaimSlots should succeed (entered purge window)
		err = tryRandomUnlock(ledger.L(0).TagAlongReclaimSlots)
		require.NoError(t, err, "random should unlock at slot TagAlongReclaimSlots")
	})
}

// TestTagAlongProduction tests tag-along output production validation rules.
// It verifies that:
// - Zero chain ID is rejected with appropriate error
// - Outputs with more than 4 constraints are rejected (tag-along lock limit)
// - HolderID and target chain controller can be the same address (allowed)
// Note: Other tests use different addresses for sender and target chain controller.
func TestTagAlongProduction(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)

	t.Run("reject zero chain ID", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 1, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]

		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		// use zero chain ID
		zeroChainID := base.ChainID{}
		taOutput := ledger.NewTagAlongOutput(fee, zeroChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		// use timestamp after the consumed output
		txb.SetTimestamp(outs[0].ID.Timestamp().AddSlots(1))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "should reject zero chain ID")
		require.True(t, strings.Contains(err.Error(), "non zero argument expected"),
			"expected error containing 'non zero argument expected', got: %v", err)
	})

	t.Run("reject too many constraints", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		// create output with tag-along lock + extra constraints (more than 4 total)
		o := ledger.NewOutput(func(ob *ledger.OutputBuilder) {
			ob.WithTokenBalance(fee)
			ob.WithLock(&ledger.TagAlongLock{
				TargetSequencerID: targetChainID,
				SenderID:          base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)),
			})
			// add extra constraints to exceed the limit of 4
			// constraint at index 0 is amount, index 1 is lock
			// adding 3 more should make 5 total, exceeding the limit
			for i := 0; i < 3; i++ {
				ob.MustPushConstraint(ledger.NewAmounts(int64(i+1), int64(i+2)).Bytes())
			}
		})
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		txb.SetTimestamp(seqOrigin.ID.Timestamp().AddSlots(2))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "should reject more than 5 UTXO elements")
		require.True(t, strings.Contains(err.Error(), "no more than 5 UTXO elements"),
			"expected error containing 'no more than 5 UTXO elements', got: %v", err)
	})

	t.Run("sender and target controller can be same", func(t *testing.T) {
		// Verify that tag-along where sender == target chain controller is allowed.
		// This is a valid use case where the chain controller pays fee to themselves.
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 1, initAmount)
		privKeyController := privKeys[0]
		addrController := addrs[0]

		// get controller outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		controllerOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrController.ControllerID())
		require.NoError(t, err)
		require.True(t, len(controllerOuts) > 0)

		// create chain controlled by addrController with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyController, addrController, controllerOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrController.ControllerID())
		require.NoError(t, err)
		// find a non-chain output to consume
		var inputOut *ledger.OutputWithID
		for _, o := range outs {
			if o.Output.Lock().Name() == ledger.SigLockName {
				inputOut = o
				break
			}
		}
		require.NotNil(t, inputOut, "should have a consumable output")

		_, err = txb.ConsumeOutput(inputOut.Output, inputOut.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		// create tag-along where sender == target chain controller (same address)
		taOutput := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(addrController))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(inputOut.Output.TokenBalance() - fee).WithLock(addrController)
		}))
		require.NoError(t, err)

		txb.SetTimestamp(seqOrigin.ID.Timestamp().AddSlots(2))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeyController)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err, "tag-along where sender == target controller should be allowed")

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// verify tag-along is in backlog
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts), "tag-along should be in backlog")
	})
}

// TestTagAlongNegativeUnlock tests that wrong parties cannot unlock tag-along outputs
// in the wrong time windows. It verifies:
// - HolderID cannot unlock during tag-along window (only target can)
// - Random party cannot unlock during tag-along window
// - Random party cannot unlock during reclaim window (only sender can)
// - Target cannot unlock during reclaim window
func TestTagAlongNegativeUnlock(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)

	// helper to setup test environment
	setup := func(t *testing.T) (*utxodb.UTXODB, ed25519.PrivateKey, ed25519.PrivateKey, ed25519.PrivateKey,
		ledger.SigLock, *ledger.OutputWithChainID, base.ChainID, base.LedgerTime) {

		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 3, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]
		privKeyRandom := privKeys[2]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		// create tag-along output
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		taTs := seqOrigin.ID.Timestamp().AddSlots(2)
		txb.SetTimestamp(taTs)
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		return u, privKeySender, privKeyTarget, privKeyRandom, addrSender, seqOrigin, targetChainID, taTs
	}

	t.Run("sender cannot unlock during tag-along window", func(t *testing.T) {
		u, privKeySender, _, _, addrSender, _, targetChainID, taTs := setup(t)

		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb := txbuilder.New()
		_, err := txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		senderOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		senderOuts = util.PurgeSlice(senderOuts, func(o *ledger.OutputWithID) bool {
			return o.Output.Lock().Name() == ledger.SigLockName
		})
		maxOut := slices.MaxFunc(senderOuts, func(a, b *ledger.OutputWithID) int {
			if a.Output.TokenBalance() < b.Output.TokenBalance() {
				return -1
			}
			if a.Output.TokenBalance() > b.Output.TokenBalance() {
				return 1
			}
			return 0
		})
		idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
		require.NoError(t, err)
		err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
			o.WithLock(addrSender)
		}))
		require.NoError(t, err)

		// try to unlock in tag-along window (slot 1)
		txb.SetTimestamp(taTs.AddSlots(1))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "sender should NOT unlock during tag-along window")
		require.True(t, strings.Contains(err.Error(), "inside tag along slots must be unlocked by the target"))
	})

	t.Run("random cannot unlock during tag-along window", func(t *testing.T) {
		u, _, _, privKeyRandom, _, _, targetChainID, taTs := setup(t)

		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		reclaimerAddr := ledger.SigLockFromED25519PrivateKey(privKeyRandom)
		reclaimerOuts, err := u.SugaredStateReader().GetOutputsForAccount(reclaimerAddr.ControllerID())
		require.NoError(t, err)
		reclaimerOuts = util.PurgeSlice(reclaimerOuts, func(o *ledger.OutputWithID) bool {
			return o.Output.Lock().Name() == ledger.SigLockName
		})

		txb := txbuilder.New()
		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		maxOut := slices.MaxFunc(reclaimerOuts, func(a, b *ledger.OutputWithID) int {
			if a.Output.TokenBalance() < b.Output.TokenBalance() {
				return -1
			}
			if a.Output.TokenBalance() > b.Output.TokenBalance() {
				return 1
			}
			return 0
		})
		idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
		require.NoError(t, err)
		err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
			o.WithLock(reclaimerAddr)
		}))
		require.NoError(t, err)

		// try to unlock in tag-along window (slot 1)
		txb.SetTimestamp(taTs.AddSlots(1))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeyRandom)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "random should NOT unlock during tag-along window")
		require.True(t, strings.Contains(err.Error(), "unlock window error") || strings.Contains(err.Error(), "inside tag along slots must be unlocked by the target"))
	})

	t.Run("random cannot unlock during reclaim window", func(t *testing.T) {
		u, _, _, privKeyRandom, _, _, targetChainID, taTs := setup(t)

		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		reclaimerAddr := ledger.SigLockFromED25519PrivateKey(privKeyRandom)
		reclaimerOuts, err := u.SugaredStateReader().GetOutputsForAccount(reclaimerAddr.ControllerID())
		require.NoError(t, err)
		reclaimerOuts = util.PurgeSlice(reclaimerOuts, func(o *ledger.OutputWithID) bool {
			return o.Output.Lock().Name() == ledger.SigLockName
		})

		txb := txbuilder.New()
		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		maxOut := slices.MaxFunc(reclaimerOuts, func(a, b *ledger.OutputWithID) int {
			if a.Output.TokenBalance() < b.Output.TokenBalance() {
				return -1
			}
			if a.Output.TokenBalance() > b.Output.TokenBalance() {
				return 1
			}
			return 0
		})
		idx, err := txb.ConsumeOutput(maxOut.Output, maxOut.ID)
		require.NoError(t, err)
		err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
			o.WithLock(reclaimerAddr)
		}))
		require.NoError(t, err)

		// try to unlock in reclaim window (middle of the window)
		txb.SetTimestamp(taTs.AddSlots(ledger.L(0).TagAlongSlots + 10))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeyRandom)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "random should NOT unlock during reclaim window")
		require.True(t, strings.Contains(err.Error(), "unlock window error") || strings.Contains(err.Error(), "inside reclaim slots must be unlocked by the sender"))
	})

	t.Run("target cannot unlock during reclaim window", func(t *testing.T) {
		u, _, privKeyTarget, _, _, seqOrigin, targetChainID, taTs := setup(t)

		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb := txbuilder.New()
		_, err := txb.ConsumeOutput(seqOrigin.Output, seqOrigin.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		_, err = txb.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

		next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(seqOrigin.Output.TokenBalance() + taOuts[0].Output.TokenBalance())
			o.WithLock(seqOrigin.Output.Lock())
			cc := ledger.NewChainConstraint(targetChainID, 0, seqOrigin.OriginSlot, 0, 0, seqOrigin.TransitionCounter+1, 0)
			o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		})
		_, err = txb.ProduceOutput(next)
		require.NoError(t, err)

		// try to unlock in reclaim window (middle of the window)
		txb.SetTimestamp(taTs.AddSlots(ledger.L(0).TagAlongSlots + 10))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeyTarget)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "target should NOT unlock during reclaim window")
		require.True(t, strings.Contains(err.Error(), "inside reclaim slots must be unlocked by the sender"))
	})
}

// TestTagAlongMultiple tests scenarios with multiple tag-along outputs.
// It verifies:
// - Multiple tag-along outputs to the same sequencer are tracked correctly in backlog
// - Multiple tag-along outputs to different sequencers in a single tx work correctly
// - Each sequencer's backlog contains only its targeted outputs
func TestTagAlongMultiple(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)

	t.Run("multiple tag-alongs to same sequencer", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		// create first tag-along output
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput1 := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput1)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		txb.SetTimestamp(seqOrigin.ID.Timestamp().AddSlots(2))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// create second tag-along output
		txb2 := txbuilder.New()
		outs2, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		// find the non-tag-along output
		var senderOut *ledger.OutputWithID
		for _, o := range outs2 {
			if o.Output.Lock().Name() == ledger.SigLockName {
				senderOut = o
				break
			}
		}
		require.NotNil(t, senderOut)

		_, err = txb2.ConsumeOutput(senderOut.Output, senderOut.ID)
		require.NoError(t, err)
		txb2.PutSignatureUnlock(0)

		taOutput2 := ledger.NewTagAlongOutput(fee*2, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb2.ProduceOutput(taOutput2)
		require.NoError(t, err)
		_, err = txb2.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(senderOut.Output.TokenBalance() - fee*2).WithLock(addrSender)
		}))
		require.NoError(t, err)

		txb2.SetTimestamp(seqOrigin.ID.Timestamp().AddSlots(3))
		txb2.ComputeInputCommitment()
		txb2.SignED25519(privKeySender)
		txBytes2, _, _, err := txb2.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes2)
		require.NoError(t, err)

		// verify both tag-alongs are in backlog
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 2, len(taOuts), "should have 2 tag-along outputs")

		// verify total amounts
		totalFee := uint64(0)
		for _, ta := range taOuts {
			totalFee += ta.Output.TokenBalance()
		}
		require.EqualValues(t, fee+fee*2, totalFee)
	})

	t.Run("multiple tag-alongs to different sequencers", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 3, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget1 := privKeys[1]
		addrTarget1 := addrs[1]
		privKeyTarget2 := privKeys[2]
		addrTarget2 := addrs[2]

		// get target1 outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		target1Outs, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget1.ControllerID())
		require.NoError(t, err)
		require.True(t, len(target1Outs) > 0)

		// create first chain with timestamp derived from actual output
		seqOrigin1, err := u.MakeNewChain(chainAmount, privKeyTarget1, addrTarget1, target1Outs[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID1 := seqOrigin1.ChainID

		// get target2 outputs to derive timestamp
		target2Outs, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget2.ControllerID())
		require.NoError(t, err)
		require.True(t, len(target2Outs) > 0)

		// create second chain with timestamp derived from actual output
		seqOrigin2, err := u.MakeNewChain(chainAmount, privKeyTarget2, addrTarget2, target2Outs[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID2 := seqOrigin2.ChainID

		// create tag-along outputs to both chains in single tx
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput1 := ledger.NewTagAlongOutput(fee, targetChainID1, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput1)
		require.NoError(t, err)

		taOutput2 := ledger.NewTagAlongOutput(fee*2, targetChainID2, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput2)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee - fee*2).WithLock(addrSender)
		}))
		require.NoError(t, err)

		ts := seqOrigin1.ID.Timestamp()
		if seqOrigin2.ID.Timestamp().After(ts) {
			ts = seqOrigin2.ID.Timestamp()
		}
		txb.SetTimestamp(ts.AddSlots(2))
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// verify backlogs for each chain
		taOuts1 := u.SugaredStateReader().GetTagAlongBacklog(targetChainID1)
		require.EqualValues(t, 1, len(taOuts1), "chain1 should have 1 tag-along output")
		require.EqualValues(t, fee, taOuts1[0].Output.TokenBalance())

		taOuts2 := u.SugaredStateReader().GetTagAlongBacklog(targetChainID2)
		require.EqualValues(t, 1, len(taOuts2), "chain2 should have 1 tag-along output")
		require.EqualValues(t, fee*2, taOuts2[0].Output.TokenBalance())
	})
}

// TestTagAlongBalanceVerification tests that balances are correctly handled
// after tag-along consumption or reclaim. It verifies:
// - Chain balance increases by fee amount after consuming tag-along (validated by chain constraint)
// - HolderID recovers full balance after reclaiming tag-along in reclaim window
// - Tag-along backlog is cleared after consumption or reclaim
func TestTagAlongBalanceVerification(t *testing.T) {
	const (
		initAmount  = 1_000_000_000_000
		fee         = 500
		chainAmount = 500_000_000_000
	)

	t.Run("verify chain balance after consumption", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		initialChainBalance := seqOrigin.Output.TokenBalance()

		// create tag-along output
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outs[0].Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		taTs := seqOrigin.ID.Timestamp().AddSlots(2)
		txb.SetTimestamp(taTs)
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// consume tag-along by target chain
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb2 := txbuilder.New()
		_, err = txb2.ConsumeOutput(seqOrigin.Output, seqOrigin.ID)
		require.NoError(t, err)
		txb2.PutSignatureUnlock(0)
		txb2.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		_, err = txb2.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb2.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

		expectedBalance := initialChainBalance + fee
		next := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(expectedBalance)
			o.WithLock(seqOrigin.Output.Lock())
			cc := ledger.NewChainConstraint(targetChainID, 0, seqOrigin.OriginSlot, 0, 0, seqOrigin.TransitionCounter+1, 0)
			o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		})
		_, err = txb2.ProduceOutput(next)
		require.NoError(t, err)

		txb2.SetTimestamp(taTs.AddSlots(1))
		txb2.ComputeInputCommitment()
		txb2.SignED25519(privKeyTarget)
		txBytes2, _, _, err := txb2.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes2)
		require.NoError(t, err)

		// Transaction succeeded, which validates that:
		// - Chain constraint accepted the new balance (initialChainBalance + fee)
		// - Tag-along was correctly consumed by the target chain

		// verify backlog is cleared
		taOutsAfter := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 0, len(taOutsAfter), "backlog should be empty after consumption")
	})

	t.Run("verify sender balance after reclaim", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, initAmount)
		privKeySender := privKeys[0]
		addrSender := addrs[0]
		privKeyTarget := privKeys[1]
		addrTarget := addrs[1]

		// get target outputs to derive timestamp (avoids timing race with ledger.TimeNow())
		targetOuts, err := u.SugaredStateReader().GetOutputsForAccount(addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)

		// create chain with timestamp derived from actual output
		seqOrigin, err := u.MakeNewChain(chainAmount, privKeyTarget, addrTarget, targetOuts[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		targetChainID := seqOrigin.ChainID

		// get initial sender balance
		senderOutsInitial, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		initialSenderBalance := uint64(0)
		for _, o := range senderOutsInitial {
			if o.Output.Lock().Name() == ledger.SigLockName {
				initialSenderBalance += o.Output.TokenBalance()
			}
		}

		// create tag-along output
		txb := txbuilder.New()
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		var senderOut *ledger.OutputWithID
		for _, o := range outs {
			if o.Output.Lock().Name() == ledger.SigLockName {
				senderOut = o
				break
			}
		}
		require.NotNil(t, senderOut)

		_, err = txb.ConsumeOutput(senderOut.Output, senderOut.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		taOutput := ledger.NewTagAlongOutput(fee, targetChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeySender)))
		_, err = txb.ProduceOutput(taOutput)
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(senderOut.Output.TokenBalance() - fee).WithLock(addrSender)
		}))
		require.NoError(t, err)

		taTs := seqOrigin.ID.Timestamp().AddSlots(2)
		txb.SetTimestamp(taTs)
		txb.ComputeInputCommitment()
		txb.SignED25519(privKeySender)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		// reclaim by sender (in reclaim window)
		taOuts := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 1, len(taOuts))

		txb2 := txbuilder.New()
		_, err = txb2.ConsumeOutput(taOuts[0].Output, taOuts[0].ID)
		require.NoError(t, err)
		txb2.PutSignatureUnlock(0)

		senderOuts2, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		senderOuts2 = util.PurgeSlice(senderOuts2, func(o *ledger.OutputWithID) bool {
			return o.Output.Lock().Name() == ledger.SigLockName
		})
		maxOut := slices.MaxFunc(senderOuts2, func(a, b *ledger.OutputWithID) int {
			if a.Output.TokenBalance() < b.Output.TokenBalance() {
				return -1
			}
			if a.Output.TokenBalance() > b.Output.TokenBalance() {
				return 1
			}
			return 0
		})
		idx, err := txb2.ConsumeOutput(maxOut.Output, maxOut.ID)
		require.NoError(t, err)
		err = txb2.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
		require.NoError(t, err)

		_, err = txb2.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(taOuts[0].Output.TokenBalance() + maxOut.Output.TokenBalance())
			o.WithLock(addrSender)
		}))
		require.NoError(t, err)

		// reclaim in reclaim window
		reclaimTs := taTs.AddSlots(ledger.L(0).TagAlongSlots + 1)
		txb2.SetTimestamp(reclaimTs)
		txb2.ComputeInputCommitment()
		txb2.SignED25519(privKeySender)
		txBytes2, _, _, err := txb2.BytesWithValidation()
		require.NoError(t, err)
		err = u.AddTransaction(txBytes2)
		require.NoError(t, err)

		// verify sender got funds back (total should equal initial)
		senderOutsFinal, err := u.SugaredStateReader().GetOutputsForAccount(addrSender.ControllerID())
		require.NoError(t, err)
		finalSenderBalance := uint64(0)
		for _, o := range senderOutsFinal {
			if o.Output.Lock().Name() == ledger.SigLockName {
				finalSenderBalance += o.Output.TokenBalance()
			}
		}
		require.EqualValues(t, initialSenderBalance, finalSenderBalance,
			"sender should recover full balance after reclaim")

		// verify backlog is cleared
		taOutsAfter := u.SugaredStateReader().GetTagAlongBacklog(targetChainID)
		require.EqualValues(t, 0, len(taOutsAfter), "backlog should be empty after reclaim")
	})
}
