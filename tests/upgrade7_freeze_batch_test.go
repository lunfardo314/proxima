package tests

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// On a genesis folding upgrades 1 to 7 a sequencer under 20,000 PROX of its own may freeze delegations afresh when
// its coverage after the milestone reaches 100,000, and the ledger judges every freeze of the
// milestone against that final coverage. A sequencer holding 15,000 of its own, with two unfrozen
// delegations of 60,000 each, reaches it only by freezing both in one milestone: each alone leaves it
// at 75,000. The builder admits them as a batch (SetCoverageBatch); without the batch the first is
// refused; a batch declared but not completed is refused before the milestone is built, so nothing
// the ledger would refuse goes out.
func TestUpgrade7FreezeBatch(t *testing.T) {
	const (
		own       = 15_000 * base.PROX
		delegated = 60_000 * base.PROX
	)
	withGenesisUpgrade7(t)

	privKey := genesisPrivateKey
	addr := ledger.SigLockFromED25519PrivateKey(privKey)
	seqID := base.RandomChainID()
	predTs := base.T(3, 50)
	ts := base.T(5, 50)

	predecessor := func() *ledger.OutputWithChainID {
		o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(own)).WithLock(addr)
			o.PutConstraint(ledger.NewChainConstraint(seqID, 0, predTs.Slot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
			o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
			o.MustPushConstraint(easyfl.InlineDataBytecode(seqdata.New().SetName("small").Bytes()))
			o.MustPushConstraint(ledger.SeqExtensionBytecode(nil))
		})
		ret, ok := ledger.AsOutputWithChainID(o, base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0))
		require.True(t, ok)
		return &ret
	}
	delegation := func() ledger.DelegationOutput {
		o := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
			Amount:               delegated,
			MasterID:             base.HolderID(addr),
			Target:               seqID,
			RequiredInflationCut: 900,
			StartSlot:            predTs.Slot,
		})
		d, ok := ledger.AsDelegationOutput(o, base.MustNewOutputID(base.RandomTransactionID(false, 2, predTs), 1))
		require.True(t, ok)
		return d
	}
	newTxb := func() *txbuilder_seq.SeqTxBuilder {
		txb, err := txbuilder_seq.New(txbuilder_seq.Params{
			Timestamp:     ts,
			Predecessor:   predecessor(),
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    privKey,
			PublicKey:     privKey.Public().(ed25519.PublicKey),
			StateReader:   multistate.DummyStateReader,
		})
		require.NoError(t, err)
		require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
		return txb
	}
	d1, d2 := delegation(), delegation()

	t.Run("one by one: under both bounds", func(t *testing.T) {
		txb := newTxb()
		_, valid, err := txb.FreezeDelegation(&d1)
		require.Error(t, err)
		require.True(t, valid, "a skip, not a permanent refusal")
		require.Contains(t, err.Error(), "under both bounds of a fresh freeze")
	})
	t.Run("as a batch: both frozen, the milestone valid", func(t *testing.T) {
		txb := newTxb()
		txb.SetCoverageBatch(2 * delegated)
		_, _, err := txb.FreezeDelegation(&d1)
		require.NoError(t, err)
		_, _, err = txb.FreezeDelegation(&d2)
		require.NoError(t, err)
		_, _, txString, err := txbtest.BuildAndValidate(txb)
		require.NoError(t, err, txString)
	})
	t.Run("a batch not completed: refused before it is built", func(t *testing.T) {
		txb := newTxb()
		txb.SetCoverageBatch(2 * delegated)
		_, _, err := txb.FreezeDelegation(&d1)
		require.NoError(t, err)
		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.Error(t, err)
		require.True(t, errors.Is(err, txbuilder_seq.ErrCoverageBatchShort), "got %v", err)
	})
}
