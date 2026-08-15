package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// Minimal reproducer: single delegation chain freeze to isolate EasyFL chain inflation validation
func TestDelegationInflationMinimal(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey)
	seqInitBalance := uint64(1_000_000_000) << 8
	pk, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 2, seqInitBalance*2)
	masterPrivateKey := pk[0]
	masterAddr := addrs[0]
	targetPrivateKey := pk[1]

	initTs := base.T(1000, 50)

	// Create sequencer chain origin (with sequencer constraint at slot 4)
	seqChainOrig, err := u.CreateSequencerChainOrigin(targetPrivateKey, initTs, seqInitBalance)
	require.NoError(t, err)
	seqID := seqChainOrig.ChainID
	t.Logf("seqID: %s", seqID.String())

	// First sequencer step (to have a proper sequencer in the state)
	ts := seqChainOrig.ID.Timestamp().AddSlots(1)
	txbSeq, err := txbuilder_seq.New(txbuilder_seq.Params{
		Timestamp:     ts,
		Predecessor:   seqChainOrig,
		Stem:          nil,
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    targetPrivateKey,
		PublicKey:     targetPrivateKey.Public().(ed25519.PublicKey),
		StateReader:   nil,
	})
	require.NoError(t, err)
	err = txbSeq.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0)))
	require.NoError(t, err)
	txBytes, _, _, err := txbtest.BuildAndValidate(txbSeq)
	require.NoError(t, err)
	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	// Create a single delegation chain origin
	const delegatedAmount = 1_000_000_000
	delegChainOrig, err := u.CreateChainOrigin(masterPrivateKey, initTs, delegatedAmount)
	require.NoError(t, err)
	delegChainID := delegChainOrig.ChainID
	t.Logf("delegChainID: %s", delegChainID.String())

	// Convert chain origin to delegation with DelegateLock
	delegOut := delegChainOrig
	{
		txb := exhelp.New()
		_, err = txb.ConsumeOutput(delegOut.Output, delegOut.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.PutConstraint(delegOut.Output.Amounts().Bytes(), ledger.ConstraintIndexAmounts)
			delegateLock := ledger.NewDelegateLock(seqID, base.HolderID(masterAddr), 980)
			o.WithLock(delegateLock)
			o.PutConstraint(ledger.NewChainConstraint(delegChainID, 0, delegOut.OriginSlot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
			o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		}))
		require.NoError(t, err)
		txb.SetTimestamp(delegOut.ID.Timestamp().AddSlots(1))
		txb.ComputeInputCommitment()
		txb.SignED25519(masterPrivateKey)
		var txStr string
		txBytes, _, txStr, err = txbtest.BuildAndValidate(txb)
		if err != nil {
			t.Logf("FAILED to create delegation output:\n%s", txStr)
		}
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)
	}

	// Now run several sequencer steps, freezing the delegation when possible
	for step := 0; step < 300; step++ {
		rdr := u.SugaredStateReader()
		seqOut, err := rdr.GetChainOutputWithID(seqID)
		require.NoError(t, err)

		ts = ts.AddSlots(1)
		t.Logf("step %d, slot %d", step, ts.Slot)

		txb, err := txbuilder_seq.NewWithSequencerID(ts, seqID, targetPrivateKey, rdr)
		require.NoError(t, err)
		err = txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0)))
		require.NoError(t, err)

		// Try freezing the delegation
		delegations, err := rdr.GetDelegationsForSequencer(seqID, func(o *ledger.DelegationOutput) bool {
			return o.IsUnlockableByTargetForFreezing(ts.Slot)
		})
		require.NoError(t, err)

		for _, dIn := range delegations {
			t.Logf("   freezing delegation %s, amounts: %s, slot: %d", dIn.ChainID.StringShort(), dIn.Output.Amounts().String(), dIn.ID.Slot())
			_, _, err = txb.FreezeDelegation(&dIn)
			require.NoError(t, err)
		}

		txBytes, _, txStr, err := txbtest.BuildAndValidate(txb)
		if err != nil {
			t.Logf("FAILED at step %d:\n%s", step, txStr)
			t.Logf("seqOut amounts: %s", seqOut.Output.Amounts().String())

			// Debug: log consumed outputs from builder
			for i, co := range txb.ConsumedOutputs {
				if co != nil {
					t.Logf("  consumed[%d] tokenBalance=%d, inflation=%d, frozenCov=%d",
						i, co.TokenBalance(), co.Inflation(), co.FrozenCoverage(0))
				}
			}
		}
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)
	}
	t.Logf("all %d steps passed", 300)
}
