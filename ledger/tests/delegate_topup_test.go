// The top-up request (kb/delegation_topup.md): one tag-along to the target
// whose whole balance goes into a delegation, frozen or not, with the target
// prepaying the advance on the newly frozen amount. The ledger side is the
// ensureTopUpDelegation constraint on the request and the referenced path of
// the delegate lock; these tests drive both through the delegation harness of
// delegate_test.go, as the allowance tests do.
package tests

import (
	"testing"

	"golang.org/x/crypto/ed25519"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

const topUpAmount = 100_000_000

// makeTopUpRequestOutput sends a top-up request the way the wallet does: a
// tag-along to the delegation's target carrying the amount, the request code
// at element 3 and the ensureTopUpDelegation at element 4.
func (td *testData) makeTopUpRequestOutput(ts base.LedgerTime, signer ed25519.PrivateKey, sender ledger.SigLock, named base.ChainID, amount uint64) *ledger.OutputWithID {
	par, err := td.u.MakeTransferInputData(signer, nil, ts)
	require.NoError(td, err)
	reqParams := smallkv.New()
	reqParams.Set(byte(0), []byte{4}) // FieldCmdCode = RequestCodeTopUpDelegation
	txBytes, err := utxodb.MakeSimpleTransferTransaction(
		par.WithAmount(amount).
			WithTargetLock(&ledger.TagAlongLock{
				TargetSequencerID: td.target,
				SenderID:          base.HolderID(sender),
			}).
			WithConstraintBinary(easyfl.InlineDataBytecode(reqParams.Bytes())).
			WithConstraint(&ledger.EnsureTopUpDelegation{ChainID: named}),
	)
	require.NoError(td, err)
	require.NoError(td, td.u.AddTransaction(txBytes))
	outs, err := td.u.SugaredStateReader().GetTagAlongBacklogForSequencer(td.target)
	require.NoError(td, err)
	for i := range outs {
		if _, idx := outs[i].Output.EnsureTopUpDelegationConstraint(); idx == 4 {
			ret := outs[i]
			return &ret
		}
	}
	require.Fail(td, "top-up request output not found")
	return nil
}

// withBalance is the output with its token balance replaced, inflation and
// frozen-coverage cells kept.
func withBalance(o *ledger.Output, balance int64, maxFrozenEpochs byte) *ledger.Output {
	a := o.Amounts()
	amounts := append([]int64{balance, int64(a.InflationAmount()), 0}, a.FrozenCoverageVector(maxFrozenEpochs)...)
	return o.Clone(func(ob *ledger.OutputBuilder) {
		ob.WithAmounts(amounts...)
	})
}

type topUpParams struct {
	ts      base.LedgerTime
	request *ledger.OutputWithID
	// 0 means a continuation of the running freeze; otherwise a fresh freeze
	// up to this epoch, for a delegation the target may freeze
	freezeUntilEpoch uint32
	// deviations from the honest transaction
	omitUnlockRef   bool // 2-byte target unlock, no reference to the request
	unlockAsMaster  bool
	successorOnHold bool                                     // put on hold instead of keeping it frozen
	tamper          func(succ *ledger.Output) *ledger.Output // replace the honest successor
}

// topUpDelegation builds the sequencer's side of a top-up: seq chain at input
// 0, the delegation at input 1, the request at input 2. Conservation is kept
// whatever the successor looks like, so a rejection is the constraint's.
func (td *testData) topUpDelegation(par topUpParams) error {
	amount := par.request.Output.TokenBalance()
	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)
	predIdx, err := txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	require.NoError(td, err)
	requestIdx, err := txb.ConsumeOutput(par.request.Output, par.request.ID)
	require.NoError(td, err)

	var succ *ledger.Output
	switch {
	case par.successorOnHold:
		succ, err = td.delegatedOutput.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
			TxTs:                     par.ts,
			PredOutputIndex:          predIdx,
			Inflation:                td.delegatedOutput.InflationOneSlot(),
			DisableConsistencyChecks: true,
		})
		require.NoError(td, err)
		succ = withBalance(succ, int64(succ.TokenBalance()+amount), td.delegatedOutput.TargetMaxFrozenEpochs())
	case par.freezeUntilEpoch == 0:
		succ, err = td.delegatedOutput.MakeDelegationTopUpOutput(par.ts, predIdx, amount)
		require.NoError(td, err)
	default:
		succ, err = td.delegatedOutput.MakeDelegationFreezeOutputWithTopUp(par.ts, par.freezeUntilEpoch, predIdx, td.delegatedOutput.RequiredInflationCut, amount, true)
		require.NoError(td, err)
	}
	if par.tamper != nil {
		succ = par.tamper(succ)
	}
	// the chain absorbs the request and pays whatever the successor holds
	// above the predecessor and its inflation
	seqBalance := td.seqChainOrigin.Output.TokenBalance() + td.delegatedOutput.Output.TokenBalance() + amount + succ.Inflation() - succ.TokenBalance()
	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	seqIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(seqBalance))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(td, err)
	additional := []byte{ledger.DelegationUnlockedByTarget}
	if par.unlockAsMaster {
		additional = []byte{ledger.DelegationUnlockedByMaster}
	}
	if !par.omitUnlockRef {
		additional = append(additional, requestIdx)
	}
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), additional...)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutUnlockParams(requestIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
	txb.PutUnlockParams(requestIdx, 4, []byte{succIdx})

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(td, err)
	txb.MustPutFrozenCoverage(seqIdx, fcDelta, par.ts)
	txb.SetSequencerData(seqIdx, txbuildercore.SequencerOutputIndexNone)
	txb.PushEndorsements(base.NewTransactionID(par.ts.AddTicks(-5), base.TransactionIDShort{}, true))
	txb.ComputeInputCommitment()
	txb.SetTimestamp(par.ts)
	txb.SignED25519(td.seqPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		return err
	}
	if err = td.u.AddTransaction(txBytes); err != nil {
		return err
	}
	td.delegatedOutput, err = td.u.SugaredStateReader().GetDelegatedOutput(td.delegatedOutput.ChainID)
	require.NoError(td, err)
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)
	return nil
}

// A continuation: the delegation stays frozen with its epoch and share, the
// balance grows by inflation, the amount and the advance on the amount over
// the remaining span, the target's frozen coverage grows by the same. A
// second top-up and an askstop afterwards both settle, which pins that the
// increase-only cells and the balance-based revoke deltas agree.
func TestTopUpContinuation(t *testing.T) {
	td := setupFrozenDelegation(t)
	before := td.delegatedOutput
	seqBefore := td.seqChainOrigin.Output
	req := td.makeTopUpRequestOutput(td.timestampSlotsForward(1), td.masterPrivateKey, td.masterAddr, before.ChainID, topUpAmount)
	ts := td.timestampSlotsForward(2)
	require.NoError(t, td.topUpDelegation(topUpParams{ts: ts, request: req}))

	after := td.delegatedOutput
	require.True(t, after.IsMarkedFrozen())
	require.Equal(t, before.LastFrozenEpoch, after.LastFrozenEpoch)
	require.Equal(t, before.AdvanceShare, after.AdvanceShare)
	_, _, frozenEpochs := before.FrozenEpochs(ts)
	advance := before.AdvanceOnAmount(topUpAmount, ts, frozenEpochs, before.AdvanceShare)
	require.Greater(t, advance, uint64(0))
	inflation := before.InflationOneSlot()
	require.EqualValues(t, before.Output.TokenBalance()+inflation+topUpAmount+advance, after.Output.TokenBalance())
	require.EqualValues(t, seqBefore.TokenBalance()-advance, td.seqChainOrigin.Output.TokenBalance())
	require.EqualValues(t, seqBefore.FrozenCoverage(0)+int64(inflation+topUpAmount+advance), td.seqChainOrigin.Output.FrozenCoverage(0))

	req2 := td.makeTopUpRequestOutput(td.timestampSlotsForward(1), td.masterPrivateKey, td.masterAddr, before.ChainID, topUpAmount)
	require.NoError(t, td.topUpDelegation(topUpParams{ts: td.timestampSlotsForward(2), request: req2}))
	require.NoError(t, td.revokeDelegation(td.timestampSlotsForward(3), true, false))
	require.True(t, td.delegatedOutput.IsMarkedOnHold())
	require.EqualValues(t, 0, td.seqChainOrigin.Output.FrozenCoverage(0), "the target's vector must be back to zero once the topped-up delegation is on hold")
}

// A fresh freeze with a top-up: the target freezes an unfrozen delegation
// and adds the amount in the same transition, prepaying the advance on the
// whole newly frozen balance.
func TestTopUpFreshFreeze(t *testing.T) {
	td := &testData{T: t}
	td.init()
	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	_, _, err := td.initDelegationUTXOMake(ts, 4, 900)
	require.NoError(t, err)
	before := td.delegatedOutput
	req := td.makeTopUpRequestOutput(td.timestampSlotsForward(1), td.masterPrivateKey, td.masterAddr, before.ChainID, topUpAmount)
	ts = td.timestampSlotsForward(2)
	until := before.FreezeUntilMax(ts)
	require.NoError(t, td.topUpDelegation(topUpParams{ts: ts, request: req, freezeUntilEpoch: until}))

	after := td.delegatedOutput
	require.True(t, after.IsMarkedFrozen())
	require.Equal(t, until, after.LastFrozenEpoch)
	frozenEpochs := until - ledger.L(ts.Slot).EpochFromSlotDirect(before.Target, ts.Slot, before.EpochSlots()) + 1
	advance := before.AdvanceOnAmount(before.Output.TokenBalance()+topUpAmount, ts, frozenEpochs, after.AdvanceShare)
	require.EqualValues(t, before.Output.TokenBalance()+before.InflationOneSlot()+topUpAmount+advance, after.Output.TokenBalance())
	require.EqualValues(t, after.Output.TokenBalance(), td.seqChainOrigin.Output.FrozenCoverage(0))
}

// What the constraints refuse, one deviation at a time.
func TestTopUpRejections(t *testing.T) {
	newReq := func(td *testData, signer ed25519.PrivateKey, sender ledger.SigLock, named base.ChainID) *ledger.OutputWithID {
		return td.makeTopUpRequestOutput(td.timestampSlotsForward(1), signer, sender, named, topUpAmount)
	}
	honest := func(td *testData) (*ledger.OutputWithID, base.LedgerTime) {
		return newReq(td, td.masterPrivateKey, td.masterAddr, td.delegatedOutput.ChainID), td.timestampSlotsForward(2)
	}
	moveBalance := func(td *testData, delta int64) func(*ledger.Output) *ledger.Output {
		return func(succ *ledger.Output) *ledger.Output {
			return withBalance(succ, int64(succ.TokenBalance())+delta, td.delegatedOutput.TargetMaxFrozenEpochs())
		}
	}

	t.Run("advance underpaid", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req, ts := honest(td)
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, tamper: moveBalance(td, -1)})
		require.ErrorContains(t, err, "wrong inflation advance")
	})
	t.Run("advance overpaid", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req, ts := honest(td)
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, tamper: moveBalance(td, 1)})
		require.ErrorContains(t, err, "wrong inflation advance")
	})
	t.Run("request consumed without the reference", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req, ts := honest(td)
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, omitUnlockRef: true})
		require.Error(t, err)
	})
	t.Run("successor put on hold with the amount", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req, ts := honest(td)
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, successorOnHold: true})
		require.ErrorContains(t, err, "delegation successor is not frozen")
	})
	t.Run("freeze span changed on a continuation", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req, ts := honest(td)
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, tamper: func(succ *ledger.Output) *ledger.Output {
			return succ.Clone(func(o *ledger.OutputBuilder) {
				st := td.delegatedOutput.DelegateLockState
				st.LastFrozenEpoch--
				o.PutConstraint(st.Bytes(), byte(succ.NumElements()-1))
			})
		}})
		require.Error(t, err)
	})
	t.Run("unlocked as master", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req, ts := honest(td)
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, unlockAsMaster: true})
		require.Error(t, err)
	})
	t.Run("sender is not the master", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		keys, _, addrs := td.u.GenerateAddresses(10, 1)
		require.NoError(t, td.u.TokensFromFaucet(addrs[0], 10*topUpAmount))
		req := newReq(td, keys[0], addrs[0], td.delegatedOutput.ChainID)
		err := td.topUpDelegation(topUpParams{ts: td.timestampSlotsForward(2), request: req})
		require.ErrorContains(t, err, "top-up not authorised by the delegation master")
	})
	t.Run("names another delegation", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		req := newReq(td, td.masterPrivateKey, td.masterAddr, base.RandomChainID())
		err := td.topUpDelegation(topUpParams{ts: td.timestampSlotsForward(2), request: req})
		require.Error(t, err)
	})
	t.Run("inside the safe revocation window", func(t *testing.T) {
		td := setupFrozenDelegation(t)
		unfreeze := td.delegatedOutput.UnfreezeSlot()
		req := td.makeTopUpRequestOutput(base.T(unfreeze+1, 5), td.masterPrivateKey, td.masterAddr, td.delegatedOutput.ChainID, topUpAmount)
		ts := base.T(unfreeze+2, 5)
		require.True(t, td.delegatedOutput.IsInSafeRevocationWindow(ts.Slot))
		err := td.topUpDelegation(topUpParams{ts: ts, request: req, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts)})
		require.ErrorContains(t, err, "safe revocation window")
	})
}

// A request the target never took is the sender's from the end of the
// tag-along window: the ensureTopUpDelegation steps aside together with the
// target's claim, so the consolidator's sweep takes it back.
func TestTopUpRequestReclaim(t *testing.T) {
	td := setupFrozenDelegation(t)
	req := td.makeTopUpRequestOutput(td.timestampSlotsForward(1), td.masterPrivateKey, td.masterAddr, td.delegatedOutput.ChainID, topUpAmount)
	lib := ledger.L(base.MaxSlot)
	wlib := walletLibFromGlobal(t)
	sweep := func(at uint32) error {
		txBytes, _, _, err := txbuildercore.MakeCompactTransaction(wlib, lib.Constants, txbuildercore.CompactParams{
			Inputs:           []txbuildercore.CompactInput{{OutputBytes: req.Output.Bytes(), ID: req.ID}},
			WalletPrivateKey: td.masterPrivateKey,
			TagAlongSeqID:    td.target,
			TagAlongFee:      500,
			TargetSlot:       at,
		})
		require.NoError(t, err)
		return td.u.AddTransaction(txBytes)
	}
	require.Error(t, sweep(req.ID.Slot()+lib.TagAlongSlots-1), "the target's window is not the sender's")
	require.NoError(t, sweep(req.ID.Slot()+lib.TagAlongSlots))
}
