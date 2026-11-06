package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// Library upgrade 6 end to end (kb/library_upgrade6_mining.md): listed after upgrades 1 to 5
// and applied by the path a node takes at start, then transactions are validated on both
// sides of its slot.

// upgrade6Slot is where these tests activate upgrade 6: past upgrade 5's slot by at least the
// minimum spacing between upgrades.
const upgrade6Slot = 510

// withUpgrade6 lists upgrades 1 to 6, applies them to the test ledger's store as a node does
// at start, and restores the package ledger afterwards.
func withUpgrade6(t *testing.T, u *utxodb.UTXODB) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
		{Slot: upgrade4Slot, Build: ledger.BuildUpgrade4Library},
		{Slot: upgrade5Slot, Build: ledger.BuildUpgrade5Library},
		{Slot: upgrade6Slot, Build: ledger.BuildUpgrade6Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, u)
}

// The upgraded library requires miner version 6 from its slot on and not before, and the
// upgrade index reaches the wallet constants.
func TestUpgrade6Library(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	withUpgrade6(t, u)

	require.EqualValues(t, 5, constAt(t, upgrade6Slot-1, "constMinerVersion"))
	require.EqualValues(t, 6, constAt(t, upgrade6Slot, "constMinerVersion"))
	require.EqualValues(t, 6, ledger.L(upgrade6Slot).Constants.UpgradeIndex)
}

// newTopUpEnv is the delegation harness on a ledger listing upgrades 1 to 6, with an
// unfrozen delegation created at slot at.
func newTopUpEnv(t *testing.T, at uint32) *testData {
	td := &testData{T: t}
	td.init()
	withUpgrade6(t, td.u)

	ts := base.T(at, 10)
	outs, total := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, delegatedTokens)
	require.True(t, total >= delegatedTokens)
	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsUnlock(outs...)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(delegatedTokens))
		o.WithLock(ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 900))
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	if total > delegatedTokens {
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - delegatedTokens)).WithLock(td.masterAddr)
		}))
		require.NoError(t, err)
	}
	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	require.NoError(t, td.u.AddTransaction(txBytes))

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)
	dc, err := tx.MustProducedOutputWithIDAt(0).AsChainOutput()
	require.NoError(t, err)
	var ok bool
	td.delegatedOutput, ok = ledger.DelegationOutputFromOutputWithChainID(dc)
	require.True(t, ok)
	return td
}

// The route the guarantor fix closes, on both sides of the slot. A sequencer
// takes a top-up request into a chain of its own: locked by sigLock to its controller,
// which ignores its unlock parameters, and carrying a delegateLockState at the end, which
// is valid on any output. The guarantor's frozen mark and 3-byte unlock are met, the
// delegate lock and its master check never run, and the request's tokens end in the
// sequencer's chain. From the slot the guarantor requires a delegateLock predecessor.
func TestUpgrade6FakeDelegation(t *testing.T) {
	for _, c := range []struct {
		name string
		slot uint32
		why  string
	}{
		{"before the slot", upgrade6Slot - 10, ""},
		{"from the slot", upgrade6Slot + 10, "predecessor must be a delegation"},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := newTopUpEnv(t, c.slot-4)
			seqCtrl := ledger.SigLockFromED25519PrivateKey(td.seqPrivateKey)

			// the sequencer controller's chain C, dressed as a delegation; funded from the
			// master's outputs, since the controller's own include the sequencer chain
			const cAmount = 100_000_000
			outs, total := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, cAmount)
			require.True(t, total >= cAmount)
			txb := exhelp.New()
			_, _, err := txb.ConsumeOutputsUnlock(outs...)
			require.NoError(t, err)
			cTs := base.T(c.slot-3, 10)
			_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithAmounts(cAmount).WithLock(seqCtrl)
				o.PutConstraint(ledger.NewChainOrigin(cTs.Slot).Bytes(), ledger.ConstraintIndexChain)
				o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
			}))
			require.NoError(t, err)
			if total > cAmount {
				_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
					o.WithAmounts(int64(total - cAmount)).WithLock(td.masterAddr)
				}))
				require.NoError(t, err)
			}
			txb.ComputeInputCommitment()
			txb.SetTimestamp(cTs)
			txb.SignED25519(td.masterPrivateKey)
			txBytes, _, _, err := txbtest.BuildAndValidate(txb)
			require.NoError(t, err)
			require.NoError(t, td.u.AddTransaction(txBytes))
			tx, err := transaction.Parse(txBytes)
			require.NoError(t, err)
			cOut, err := tx.MustProducedOutputWithIDAt(0).AsChainOutput()
			require.NoError(t, err)

			// a request naming C, sent by the sequencer's own side
			req := td.makeTopUpRequestOutput(base.T(c.slot-2, 10), td.masterPrivateKey, td.masterAddr, cOut.ChainID, topUpAmount)

			// the sequencer's transaction: seq chain at 0, C at 1, the request at 2
			ts := base.T(c.slot, 20)
			txb = exhelp.New()
			_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
			require.NoError(t, err)
			cIdx, err := txb.ConsumeOutput(cOut.Output, cOut.ID)
			require.NoError(t, err)
			reqIdx, err := txb.ConsumeOutput(req.Output, req.ID)
			require.NoError(t, err)
			seqIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
				o.PutConstraint(ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
			}))
			require.NoError(t, err)
			cSucc := cOut.Output.Clone(func(o *ledger.OutputBuilder) {
				o.WithAmounts(int64(cAmount + topUpAmount))
				o.PutConstraint(ledger.NewChainConstraint(cOut.ChainID, cIdx, cTs.Slot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
				o.PutConstraint(ledger.DelegateLockState{LastFrozenEpoch: 1, State: ledger.DelegateLockStateFrozen}.Bytes(), byte(cOut.Output.NumElements()-1))
			})
			cSuccIdx, err := txb.ProduceOutput(cSucc)
			require.NoError(t, err)
			txb.PutSignatureUnlock(0)
			txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(seqIdx))
			// three bytes on C's sigLock: no reference (0xff), anything, the request
			txb.PutUnlockParams(cIdx, ledger.ConstraintIndexLock, []byte{0xff, 0, reqIdx})
			txb.PutUnlockParams(cIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(cSuccIdx))
			txb.PutUnlockParams(reqIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
			txb.PutUnlockParams(reqIdx, 4, []byte{cSuccIdx})
			fcDelta, err := txb.CalcFrozenCoverageDelta()
			require.NoError(t, err)
			txb.MustPutFrozenCoverage(seqIdx, fcDelta, ts)
			txb.SetSequencerData(seqIdx, txbuildercore.SequencerOutputIndexNone)
			txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))
			txb.ComputeInputCommitment()
			txb.SetTimestamp(ts)
			txb.SignED25519(td.seqPrivateKey)
			txBytes, _, _, err = txbtest.BuildAndValidate(txb)
			if err == nil {
				err = td.u.AddTransaction(txBytes)
			}
			if c.why == "" {
				require.NoError(t, err, "the route is open before the slot")
				return
			}
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, c.why))
		})
	}
}
