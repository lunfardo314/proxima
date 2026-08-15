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
	// Place the origin at a deterministic mid-slot tick. utxodb stamps its genesis/faucet
	// outputs from ledger.TimeNow(), so the inherited tick is wall-clock-random, and the
	// subtests build transitions at fixed tick offsets around this origin. Both slot ends
	// are hazardous: a high tick pushes a pace-forward multi-input sequencer transition into
	// the pre-branch consolidation zone (the last ticks of a slot), which the sequencer
	// constraint forbids; a low tick pushes a dummy-endorsement built at ts.AddTicks(-N)
	// across the slot boundary (cross-slot endorsements are forbidden). Tick 40 clears both.
	// Two slots ahead of the inputs keeps a full-slot gap so the origin tx satisfies pace.
	originTs := base.T(parsed[0].ID.Timestamp().Slot+2, 40)

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
		// chain origin: coverageDelta starts at 0.
		o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
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
