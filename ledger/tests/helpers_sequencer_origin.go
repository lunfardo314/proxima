package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ed25519"
)

// mustMakeSequencerChainOrigin issues a sequencer transaction that
// produces a sequencer chain origin holding `amount` tokens locked to
// `addr`. Any leftover input balance is returned as change locked to
// the same address. The producing tx satisfies the sequencer constraint
// at slot 4 by:
//   - setting sequencer data (txSequencerOutputIndex == chain output index)
//   - endorsing a dummy sequencer tx (chain origin has no predecessor,
//     so _noChainPredecessorCase requires an endorsement)
//
// The dummy endorsement passes Stage-3 validation in utxodb because
// utxodb does not enforce endorsement-target existence; this is a
// test-only shortcut.
func mustMakeSequencerChainOrigin(
	t *testing.T,
	u *utxodb.UTXODB,
	privKey ed25519.PrivateKey,
	addr ledger.SigLock,
	amount uint64,
) ledger.OutputWithChainID {
	t.Helper()

	outs, err := u.StateReader().GetUTXOsForController(addr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outs)

	parsed := make([]*ledger.OutputWithID, len(outs))
	for i, od := range outs {
		parsed[i], err = od.Parse()
		require.NoError(t, err)
	}
	originTs := parsed[0].ID.Timestamp().AddSlots(1)

	txb := exhelp.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(parsed...)
	require.NoError(t, err)
	for i := range parsed {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			require.NoError(t, err)
		}
	}

	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(addr)
		o.MustPushConstraint(ledger.NewChainOrigin(originTs.Slot).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(
			ledger.L(originTs.Slot).DelegationEpochSlots,
			byte(ledger.L(originTs.Slot).MaxFrozenEpochs),
		).Bytes())
	})
	chainIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	if total > amount {
		changeOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - amount)).WithLock(addr)
		})
		_, err = txb.ProduceOutput(changeOut)
		require.NoError(t, err)
	}

	txb.SetSequencerData(chainIdx, txbuildercore.SequencerOutputIndexNone)
	txb.SetTimestamp(originTs)
	dummyEnd := base.NewTransactionID(originTs.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyEnd)
	txb.ComputeInputCommitment()
	txb.SignED25519(privKey)

	txBytes := txb.Bytes()
	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	originTx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	originOutputID, err := base.NewOutputID(originTx.ID(), chainIdx)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(originOutputID)

	chs, err := u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	parsedChainOut, err := chs.Parse()
	require.NoError(t, err)
	chainOriginOut, err := parsedChainOut.AsChainOutput()
	require.NoError(t, err)
	return *chainOriginOut
}
