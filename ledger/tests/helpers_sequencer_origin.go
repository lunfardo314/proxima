package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ed25519"
)

// mustMakeSequencerChainOrigin issues a sequencer transaction that produces a
// sequencer chain origin holding `amount` tokens locked to `addr`. Any leftover
// input balance is returned as change locked to the same address. The producing
// tx satisfies the sequencer constraint at slot 4 by setting sequencer data
// (txSequencerOutputIndex == chain output index). _noChainPredecessorCase
// FORBIDS endorsements at chain origin, so the tx is endorsement-free.
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

	// No SetSequencerData call: the producing tx is a regular wallet tx (no `s` bit).
	// The easyfl `sequencer` constraint skips the milestone-index check at chain origin.
	// No endorsements either — the origin is pulled into the tangle via its tag-along
	// (or via a sequencer transitioning the chain output in a follow-up tx).
	txb.SetTimestamp(originTs)
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
