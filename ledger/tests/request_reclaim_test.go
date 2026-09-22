// A sequencer request the target never took is the wallet's own tokens
// sitting in a tag-along. The consolidator sweeps it back through the
// spendable classifier, so the classifier's answer must match what the ledger
// accepts: a plain request from the end of the sequencer's window, an askstop
// request (ensureStopDelegation at element 4) only from
// constTagAlongReclaimSlots, when its constraint steps aside.
package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

const (
	rrInitAmount = 1_000_000_000_000
	rrRequest    = 100_000_000
	rrTagFee     = 500
)

type requestReclaimEnv struct {
	u          *utxodb.UTXODB
	priv       ed25519.PrivateKey
	holderID   base.HolderID
	lib        *txbuildercore.Library[any]
	seedSlot   uint32
	plain      *ledger.OutputWithID // request with the payload only
	askstop    *ledger.OutputWithID // request carrying ensureStopDelegation
	tagAlongID base.ChainID
}

// makeRequestReclaimEnv sends, in one wallet-signed transaction, a plain
// request and an askstop-shaped request to a sequencer that will never take
// them.
func makeRequestReclaimEnv(t *testing.T) *requestReclaimEnv {
	t.Helper()
	env := &requestReclaimEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(29, 1, rrInitAmount)
	env.priv = privKeys[0]
	env.holderID = base.HolderID(ledger.SigLockFromED25519PrivateKey(env.priv))
	env.lib = walletLibFromGlobal(t)
	env.tagAlongID = base.RandomChainID()

	outs, err := env.u.SugaredStateReader().GetOutputsForAccount(addrs[0].ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outs)
	in := outs[0]
	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	plainReq, err := env.lib.NewSequencerRequestOutput(rrRequest, env.tagAlongID, env.holderID, 1, nil)
	require.NoError(t, err)
	ens, err := env.lib.NewEnsureStopDelegationConstraint(base.RandomChainID(), 0)
	require.NoError(t, err)
	askstopReq, err := env.lib.NewSequencerRequestOutput(rrRequest, env.tagAlongID, env.holderID, 3, nil, ens)
	require.NoError(t, err)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(in.Output, in.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	produced := make([]*ledger.Output, 0, 3)
	for _, b := range [][]byte{plainReq.Bytes(), askstopReq.Bytes()} {
		o, err := ledger.OutputFromBytes(b)
		require.NoError(t, err)
		produced = append(produced, o)
	}
	produced = append(produced, ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(in.Output.TokenBalance() - 2*rrRequest).WithLock(addrs[0])
	}))
	ids := make([]*ledger.OutputWithID, 0, len(produced))
	for _, o := range produced {
		_, perr := txb.ProduceOutput(o)
		require.NoError(t, perr)
		ids = append(ids, &ledger.OutputWithID{Output: o})
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.priv)
	txBytes, txid, failed, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "seed tx must validate:\n%s", failed)
	require.NoError(t, env.u.AddTransaction(txBytes))

	env.seedSlot = ts.Slot
	for i := range ids {
		ids[i].ID = base.MustNewOutputID(txid, byte(i))
	}
	env.plain, env.askstop = ids[0], ids[1]
	return env
}

// sweep composes the consolidator's kind of transaction over one request
// output at targetSlot and reports whether the ledger accepts it.
func (env *requestReclaimEnv) sweep(t *testing.T, o *ledger.OutputWithID, targetSlot uint32) error {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	txBytes, _, _, err := txbuildercore.MakeCompactTransaction(env.lib, lib.Constants, txbuildercore.CompactParams{
		Inputs:           []txbuildercore.CompactInput{{OutputBytes: o.Output.Bytes(), ID: o.ID}},
		WalletPrivateKey: env.priv,
		TagAlongSeqID:    env.tagAlongID,
		TagAlongFee:      rrTagFee,
		TargetSlot:       targetSlot,
	})
	require.NoError(t, err)
	return env.u.AddTransaction(txBytes)
}

func (env *requestReclaimEnv) classify(t *testing.T, o *ledger.OutputWithID, targetSlot uint32) txbuildercore.SpendClass {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	cls, err := txbuildercore.ClassifySpendable(lib, o.Output.Bytes(), o.ID.Slot(), env.holderID, targetSlot, lib.TagAlongSlots, lib.TagAlongReclaimSlots)
	require.NoError(t, err)
	return cls
}

// A plain request is swept as soon as the sequencer's window closes.
func TestRequestReclaimPlain(t *testing.T) {
	env := makeRequestReclaimEnv(t)
	lib := ledger.L(base.MaxSlot)
	at := env.seedSlot + lib.TagAlongSlots
	require.Equal(t, txbuildercore.SpendSimple, env.classify(t, env.plain, at))
	require.NoError(t, env.sweep(t, env.plain, at), "plain request must be reclaimable by the sender after the tag-along window")
}

// An askstop request is withheld until constTagAlongReclaimSlots, and the
// ledger agrees: a sweep composed earlier is rejected, one at the boundary
// settles.
func TestRequestReclaimAskstop(t *testing.T) {
	env := makeRequestReclaimEnv(t)
	lib := ledger.L(base.MaxSlot)
	early := env.seedSlot + lib.TagAlongReclaimSlots - 1
	require.Equal(t, txbuildercore.SpendNotForAccount, env.classify(t, env.askstop, early))
	require.Error(t, env.sweep(t, env.askstop, early), "ensureStopDelegation must still bind the sender before the reclaim window")

	at := env.seedSlot + lib.TagAlongReclaimSlots
	require.Equal(t, txbuildercore.SpendSimple, env.classify(t, env.askstop, at))
	require.NoError(t, env.sweep(t, env.askstop, at), "askstop request must be reclaimable by the sender once ensureStopDelegation steps aside")
}
