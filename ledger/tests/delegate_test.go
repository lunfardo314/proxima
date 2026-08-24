// Delegation tests: the delegateLock constraint end to end.
//
// Sections, in the order a delegation lives:
//
//  1. Environment and origin — the sequencer chain that is the delegation
//     target, delegation origins built directly and through the wallet
//     helpers, master and target transits, freezing and revocation, and the
//     frozen-coverage vector.
//
//  2. Attack vectors — what each unlock mode must refuse: master unlock
//     without the master signature, target unlock that shrinks the amount,
//     swaps the lock, discontinues the chain, or moves faster than a slot.
//
//  3. Epoch parameters (claude/archive/shipped/delegation_epoch_params.md) — bounds at the
//     target chain origin, immutability across transit, delegating a foundry
//     chain (Option C: delegateLockState pinned to the last tuple position,
//     foundry and any foundryPolicy carried over byte-equal), and what the
//     target may do to a foundry once it is delegated.
//
//  4. Allowance — the delegator-signed askstop allowance that lets the
//     sequencer charge the stop compensation to the delegation balance
//     (claude/archive/shipped/delegation_allowance.md).
//
//  5. Target scope — what the target may change on the output it transits:
//     amounts, chain constraint and delegateLockState, and nothing else.
//

package tests

import (
	"fmt"
	"golang.org/x/crypto/ed25519"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// ==========================================================================
// 1. Environment and origin
// ==========================================================================

const (
	tokensFromFaucetMaster        = 200_000_000_000
	tokensFromFaucetSeqController = 200_000_000_000
	seqOnChainBalance             = 3_000_000_000
	delegatedTokens               = 1_000_000_000
)

type testData struct {
	*testing.T
	u          *utxodb.UTXODB
	target     base.ChainID
	masterAddr ledger.SigLock

	seqPrivateKey, masterPrivateKey ed25519.PrivateKey
	seqChainOrigin                  ledger.OutputWithChainID
	delegatedOutput                 ledger.DelegationOutput
}

func (td *testData) init() {
	td.u = utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := td.u.GenerateAddresses(0, 2)
	td.masterPrivateKey = privKey[0]
	td.masterAddr = addr[0]
	td.seqPrivateKey = privKey[1]
	seqControllerAddr := addr[1]

	err := td.u.TokensFromFaucet(td.masterAddr, tokensFromFaucetMaster)
	require.NoError(td, err)
	err = td.u.TokensFromFaucet(seqControllerAddr, tokensFromFaucetSeqController)
	require.NoError(td, err)

	// Build a sequencer chain origin via a proper sequencer transaction.
	// The sequencer constraint at slot 4 requires the producing tx to be
	// a sequencer tx (SetSequencerData) and chain origins must endorse
	// another sequencer tx (use a dummy endorsement that wouldn't validate
	// in a real network but satisfies the in-memory constraint).
	td.seqChainOrigin = mustMakeSequencerChainOrigin(td.T, td.u, td.seqPrivateKey, seqControllerAddr, seqOnChainBalance)
	td.Logf("seq chain origin:\n%s", td.seqChainOrigin.String())

	td.target = td.seqChainOrigin.ChainID
	td.Logf("==== master address    : %s (%s)", td.masterAddr.String(), util.Th(td.u.Balance(td.masterAddr)))
	td.Logf("==== seq controller    : %s (%s)", seqControllerAddr.String(), util.Th(td.u.Balance(seqControllerAddr)))
	_, onChain, err := td.u.BalanceOnChain(td.seqChainOrigin.ChainID)
	require.NoError(td, err)
	td.Logf("==== seq on-chain      : %s", util.Th(onChain))
	td.Logf("==== delegation target : %s (%s)", td.target.String(), util.Th(td.u.Balance(ledger.ChainLockFromChainID(td.target))))
}

func (td *testData) delegationOriginDirect(ts base.LedgerTime, revoked bool, maxFrozenEpochs byte, inflationCut uint16, prnOnError bool) ([]byte, error) {
	var txBytes []byte

	// create delegation output
	par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
	if err != nil {
		return nil, err
	}

	// Phase 2 of delegation_epoch_params: inline the target chain's
	delegationLock := ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), inflationCut)
	s := ledger.DelegateLockStateUndef
	if revoked {
		s = ledger.DelegateLockStateOnHold
	}
	txBytes, err = utxodb.MakeSimpleTransferTransaction(
		par.WithAmount(delegatedTokens).
			WithTargetLock(delegationLock).
			WithConstraint(ledger.NewChainOrigin(ts.Slot)).
			WithConstraint(ledger.DelegateLockState{State: s}),
	)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err = td.u.AddTransaction(txBytes); err == nil {
		outs, err := td.u.SugaredStateReader().GetOutputsDelegatedToAccount2(td.target[:])
		require.NoError(td, err)
		require.EqualValues(td, 1, len(outs))
		td.delegatedOutput, ok = ledger.DelegationOutputFromOutputWithChainID(outs[0])
		require.True(td, ok)
		td.Logf("delegation ChainID: %s", td.delegatedOutput.ChainID.String())
		td.Logf("delegated UTXO:\n%s", td.delegatedOutput.Output.ToSource("     "))
	} else {
		if prnOnError {
			td.Logf(">>>>> %v\n============ transaction ==============\n%s", err, td.u.TxToSource(txBytes))
		}
	}
	return txBytes, err

}

func TestDelegationLock2Init(t *testing.T) {
	require.EqualValues(t, 60, ledger.L(0).SafeRevocationSlots)

	td := &testData{T: t}
	var err error

	t.Run("ok 1", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, err = td.delegationOriginDirect(ts, false, 1, 0, true)
		require.NoError(t, err)
	})
	t.Run("ok 2", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, err = td.delegationOriginDirect(ts, false, 4, 1000, true)
		require.NoError(t, err)
	})
	t.Run("fail 1", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, err = td.delegationOriginDirect(ts, true, 1, 1200, true)
		require.NoError(t, util.MustErrorWith(err, "max required inflation cut must be in promille less or equal than 1000"))
	})
}

const tagAlongFee = 500

func (td *testData) initDelegationUTXOMake(ts base.LedgerTime, maxFrozenEpochs byte, inflationCut uint16) ([]byte, string, error) {
	outs, availableTokens := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, delegatedTokens+tagAlongFee)
	require.True(td, availableTokens >= delegatedTokens+tagAlongFee)

	txBytes, err := utxodb.MakeDelegationInitTransaction(utxodb.MakeDelegationInitTransactionParams{
		Timestamp:            ts,
		Amount:               delegatedTokens,
		MasterID:             base.HolderID(td.masterAddr),
		Target:               td.target,
		RequiredInflationCut: inflationCut,
		MasterPrivateKey:     td.masterPrivateKey,
		Inputs:               outs,
		TagAlongSequencer:    base.RandomChainID(),
		TagAlongFee:          tagAlongFee,
	})
	txString := td.u.TxToSource(txBytes)
	if err != nil {
		return nil, txString, err
	}
	tx, err := transaction.Parse(txBytes)
	require.NoError(td, err)
	do := tx.MustProducedOutputWithIDAt(0)
	dc, err := do.AsChainOutput()
	require.NoError(td, err)
	var ok bool
	td.delegatedOutput, ok = ledger.DelegationOutputFromOutputWithChainID(dc)
	require.True(td, ok)

	err = td.u.AddTransaction(txBytes)
	return txBytes, txString, err

}

type transitWithMakeParams struct {
	ts                       base.LedgerTime
	freezeUntilEpoch         uint32
	inflate                  bool
	prntx                    bool
	disableConsistencyChecks bool
}

func (td *testData) transitChainWithDelegationWithMake(n int, par transitWithMakeParams) (err error) {
	requiredAdvance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(par.ts, par.freezeUntilEpoch)
	if err != nil {
		return err
	}
	delegatedOut, err := td.delegatedOutput.MakeDelegationFreezeOutput(par.ts, par.freezeUntilEpoch, 1, td.delegatedOutput.RequiredInflationCut, par.disableConsistencyChecks)
	if err != nil {
		return err
	}

	txb := exhelp.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	seqChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance() - requiredAdvance))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predOutputIndex, err := txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	require.NoError(td, err)
	require.EqualValues(td, 1, predOutputIndex)

	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	require.NoError(td, err)

	_, err = txb.ProduceOutput(delegatedOut)
	require.NoError(td, err)

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(td, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, par.ts)

	// The seq chain successor at seqChainIdx carries the sequencer
	// constraint at slot 4, which requires the producing tx to be a
	// sequencer tx. Dummy endorsement satisfies _crossSlotPredecessorCase.
	txb.SetSequencerData(seqChainIdx, txbuildercore.SequencerOutputIndexNone)
	dummyTxId := base.NewTransactionID(par.ts.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(par.ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		if par.prntx {
			err = fmt.Errorf("error: '%v'\n---------------- failing tx --------------\n%s", err, txString)
		}
		return
	}
	if par.prntx {
		td.Logf("------------- valid transaction -------------:\n%s", txString)
	}
	err = td.u.AddTransaction(txBytes)

	// get delegation tips
	td.delegatedOutput, err = td.u.SugaredStateReader().GetDelegatedOutput(td.delegatedOutput.ChainID)
	require.NoError(td, err)
	if par.prntx {
		td.Logf("%s", td.delegatedOutput.LinesSourceFull("     ").String())
	}

	// get chain tip
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)

	return
}

func (td *testData) revokeDelegation(ts base.LedgerTime, inflate, prntx bool) (err error) {
	require.NoError(td, err)

	lib := ledger.L(0)
	diffSlots := ts.Slot - td.delegatedOutput.Timestamp().Slot
	diffEpochs := lib.DiffEpochs(td.delegatedOutput.Target, ts, td.delegatedOutput.Timestamp(), td.delegatedOutput.EpochSlots())
	td.Logf(">>>> revoke -----\nts = %s, diffSlots = %d, diffEpochs = %d\n-----\n%s",
		ts.String(), diffSlots, diffEpochs, td.delegatedOutput.LinesSourceFull("   ").String())

	txb := exhelp.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	succChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	inflation := uint64(0)
	if inflate {
		inflation = lib.ChainInflationOneSlot(td.delegatedOutput.Output.TokenBalance(), uint32(td.delegatedOutput.Timestamp().Slot))
	}
	delegatedOutPar := ledger.MakeDelegationRevokeOutputParams{
		TxTs:                     ts,
		Inflation:                inflation,
		DisableConsistencyChecks: true,
	}
	delegatedOutPar.PredOutputIndex, err = txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	delegatedOut, err := td.delegatedOutput.MakeDelegationRevokeOutput(delegatedOutPar)
	require.NoError(td, err)

	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	require.NoError(td, err)

	_, err = txb.ProduceOutput(delegatedOut)
	require.NoError(td, err)

	frozenCoverageDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(td, err)

	txb.MustPutFrozenCoverage(succChainIdx, frozenCoverageDelta, ts)

	// Sequencer-chain successor at succChainIdx requires this tx to be a
	// sequencer tx; cross-slot predecessor case needs an endorsement.
	txb.SetSequencerData(succChainIdx, txbuildercore.SequencerOutputIndexNone)
	dummyTxId := base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		if prntx {
			err = fmt.Errorf("error: '%v'\n---------------- failing tx --------------\n%s", err, txString)
		}
		return
	}
	if prntx {
		td.Logf("------------- valid transaction -------------:\n%s", txString)
	}
	err = td.u.AddTransaction(txBytes)

	// get delegation tips
	td.delegatedOutput, err = td.u.SugaredStateReader().GetDelegatedOutput(td.delegatedOutput.ChainID)
	require.NoError(td, err)
	if prntx {
		td.Logf("%s", td.delegatedOutput.LinesSourceFull("     ").String())
	}

	// get chain tip
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)

	return
}

func (td *testData) timestampTicksForward(ticks int) base.LedgerTime {
	ts := base.MaximumTime(td.seqChainOrigin.Timestamp(), td.delegatedOutput.Timestamp())
	return ts.AddTicks(ticks)
}

func (td *testData) timestampSlotsForward(slots uint32) base.LedgerTime {
	ts := base.MaximumTime(td.seqChainOrigin.Timestamp(), td.delegatedOutput.Timestamp())
	return ts.AddSlots(slots)
}

func (td *testData) discontinueDelegation(ts base.LedgerTime, prntx bool) error {

	txb := exhelp.New()

	amount, _, err := txb.ConsumeOutputsNoUnlock(&td.delegatedOutput.OutputWithID)
	require.NoError(td, err)

	txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0xff, 0xff})
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount - tagAlongFee)).WithLock(td.masterAddr)
	}))
	require.NoError(td, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(tagAlongFee).WithLock(ledger.ChainLockFromChainID(base.RandomChainID()))
	}))
	_, err = txb.ProduceOutput(ledger.NewTagAlongOutput(tagAlongFee, base.RandomChainID(), base.HolderID(td.masterAddr)))
	require.NoError(td, err)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.masterPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		if prntx {
			td.Logf("error: %v\n--------- failing tx -------\n%s", err, txString)
		}
		return err
	}
	if prntx {
		td.Logf("------------- valid tx ---\n%s", txString)
	}
	return td.u.AddTransaction(txBytes)
}

func TestDelegationLockConsume(t *testing.T) {
	td := &testData{T: t}

	var err error
	var txString string
	_ = txString

	t.Run("init", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)
		td.Logf("---------------- transaction -----------------\n%s", txString)
	})
	t.Run("master+init+kill", func(t *testing.T) {
		// create delegation output and destroy it with next tx
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, 0)
		require.NoError(t, err)
		unfreezeSlot := td.delegatedOutput.UnfreezeSlot()
		//ts = base.T(uint32(unfreezeSlot-1), 10)

		ts = td.delegatedOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		t.Logf("ts: %s, unfreeze: %d", ts.String(), unfreezeSlot)

		err = td.discontinueDelegation(ts, true)
		require.NoError(t, err)
	})
	t.Run("target+init", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       td.timestampSlotsForward(1),
			freezeUntilEpoch:         0,
			inflate:                  true,
			prntx:                    true,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)
	})
	t.Run("target+init+fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       td.timestampTicksForward(int(ledger.L(0).TransactionPace)),
			freezeUntilEpoch:         0,
			prntx:                    true,
			disableConsistencyChecks: true,
		})
		require.NoError(t, util.MustErrorWith(err, "delegation successor timestamp must be at least 1 slot after"))
	})
	t.Run("target_freeze_ok", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1, 0)
		require.NoError(t, err)

		//t.Logf("=========\n%s", td.delegatedOutput.OutputWithID.String())

		ts = td.timestampSlotsForward(1000)
		txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, ts.Slot, td.delegatedOutput.EpochSlots())
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)
		frozenEpochs := freezeUntilEpoch - txEpoch + 1
		frozenSlots := ledger.L(0).FrozenSlotsFromFrozenEpochs(td.delegatedOutput.Target, ts.Slot, td.delegatedOutput.EpochSlots(), byte(frozenEpochs))
		t.Logf(">>>>>>>>> freezeUntilEpoch: %d, frozenEpochs: %d, frozenSlots: %d", freezeUntilEpoch, frozenEpochs, frozenSlots)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)
	})
	t.Run("wrong_last_frozen_epoch", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1, 0)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(500)
		txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, uint32(ts.Slot), td.delegatedOutput.EpochSlots())
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch + 1,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, util.MustErrorWith(err, "frozen epochs (61) exceed maximum 60"))
	})
	t.Run("target_freeze_ok_inflate", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(100)
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)
		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			inflate:                  true,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)
	})
	t.Run("target_freeze_ok_add_amount", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, ts.Slot, td.delegatedOutput.EpochSlots())
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)
		frozen := freezeUntilEpoch - txEpoch + 1
		_ = frozen
		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)
	})
	t.Run("master_unlock_frozen", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    true,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)

		unfreeze := td.delegatedOutput.UnfreezeSlot()

		ts = base.T(unfreeze-100, 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, util.MustErrorWith(err, "frozen output cannot be unlocked by master"))

		ts = base.T(unfreeze-1, 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, util.MustErrorWith(err, "frozen output cannot be unlocked by master"))

		ts = base.T(unfreeze, 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, err)
	})
	t.Run("revoke", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)

		// freeze for 512 slots
		ts = td.timestampSlotsForward(1)
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)
		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    false,
			disableConsistencyChecks: false,
		})
		require.NoError(t, err)

		unfreeze := td.delegatedOutput.UnfreezeSlot()

		// fail to unlock by master
		ts = base.T(unfreeze-10, 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, util.MustErrorWith(err, "frozen output cannot be unlocked by master"))

		// succeed to unlock by target to mark output revoked
		err = td.revokeDelegation(ts, false, true)
		require.NoError(t, err)

		// fail to unlock by target revoked delegation
		ts = td.timestampSlotsForward(20)
		err = td.revokeDelegation(ts, false, false)
		require.NoError(t, util.MustErrorWith(err, "on hold delegation cannot be unlocked by the target"))

		// succeed to kill the delegation chain by master
		ts = td.timestampSlotsForward(1000)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, err)

	})
	t.Run("revoke-inflate", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)

		// freeze for 512 slots
		ts = td.timestampSlotsForward(1)
		freezeUntil := td.delegatedOutput.FreezeUntilMax(ts)
		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntil,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)

		unfreeze := td.delegatedOutput.UnfreezeSlot()

		// fail to unlock by master
		ts = base.T(unfreeze-10, 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, util.MustErrorWith(err, "'frozen output cannot be unlocked by master"))

		// succeed to unlock by target to mark output revoked
		err = td.revokeDelegation(ts, true, false)
		require.NoError(t, err)

		// fail to unlock by target revoked delegation
		ts = td.timestampSlotsForward(20)
		err = td.revokeDelegation(ts, true, false)
		require.NoError(t, util.MustErrorWith(err, "on hold delegation cannot be unlocked by the target"))

		// succeed to kill the delegation chain by master
		ts = td.timestampSlotsForward(1000)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, err)
	})
}

type transitRawParams struct {
	ts                      base.LedgerTime
	frozenEpochs            byte
	successorFrozenCoverage []int64
	sequencerFrozenCoverage []int64
	inflationAdvance        uint64
	advanceShare            uint16
	prntx                   bool
}

// amountsWithFrozenCoverage is the logical amounts vector of a chain successor:
// balance, no inflation, the bound cell NewAmounts derives, and then the frozen
// coverage of as many epochs as the prefix has cells.
func amountsWithFrozenCoverage(balance int64, frozenCoverage []int64) []int64 {
	return append([]int64{balance, 0, 0}, frozenCoverage...)
}

func (td *testData) transitChainWithDelegationRaw(par transitRawParams) (err error) {
	txb := exhelp.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID, &td.delegatedOutput.OutputWithID)
	util.AssertNoError(err)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	// Re-emit the sequencer constraint with the predecessor's immutable params
	// (epochSlots/maxFrozenEpochs) and a strictly-advancing coverageDelta
	// (predecessor + 1) so the within-slot coverage-advance rule passes. The
	// exact value isn't consensus-checked under utxodb settlement.
	predSeq, predSeqIdx := td.seqChainOrigin.Output.SequencerConstraint()
	util.Assertf(predSeqIdx != 0xff, "transitChainWithDelegationRaw: predecessor is not a sequencer chain")
	succSeq := ledger.NewSequencerConstraint(predSeq.CoverageDelta + 1)

	amounts := amountsWithFrozenCoverage(int64(td.seqChainOrigin.Output.TokenBalance()-par.inflationAdvance), par.sequencerFrozenCoverage)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.seqChainOrigin.Output.Lock())
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
		idxSeq := o.MustPushConstraint(succSeq.Bytes())
		util.Assertf(idxSeq == ledger.SequencerConstraintFixedIndex, "idxSeq == SequencerConstraintFixedIndex")
	}))
	util.AssertNoError(err)

	amounts = amountsWithFrozenCoverage(int64(td.delegatedOutput.Output.TokenBalance()+par.inflationAdvance), par.successorFrozenCoverage)

	cc := ledger.NewChainConstraint(td.delegatedOutput.ChainID, 1, td.delegatedOutput.OriginSlot, 0, 0, td.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.delegatedOutput.Output.Lock())
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		freezeUntil := uint32(0)
		if par.frozenEpochs > 0 {
			txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, par.ts.Slot, td.delegatedOutput.EpochSlots())
			freezeUntil = txEpoch + uint32(par.frozenEpochs) - 1
		}
		o.MustPushConstraint(ledger.DelegateLockState{
			LastFrozenEpoch: freezeUntil,
			State:           ledger.DelegateLockStateFrozen,
			AdvanceShare:    par.advanceShare,
		}.Bytes())
	}))
	util.AssertNoError(err)

	// unlock
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	require.NoError(td, err)

	dummyTxId := base.NewTransactionID(par.ts.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(par.ts)
	txb.SetSequencerData(0, txbuildercore.SequencerOutputIndexNone)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		if par.prntx {
			err = fmt.Errorf("error: '%v'\n---------------- failing tx --------------\n%s", err, txString)
		}
		return
	}
	if par.prntx {
		td.Logf("------------- valid transaction -------------:\n%s", txString)
	}
	err = td.u.AddTransaction(txBytes)

	// get delegation tips
	td.delegatedOutput, err = td.u.SugaredStateReader().GetDelegatedOutput(td.delegatedOutput.ChainID)
	require.NoError(td, err)
	if par.prntx {
		td.Logf("%s", td.delegatedOutput.LinesSourceFull("     ").String())
	}

	// get chain tip
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)

	return
}

func TestFrozenCoverage1(t *testing.T) {
	td := &testData{T: t}
	var err error
	var txString string
	_ = txString

	t.Run("init", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      td.timestampSlotsForward(1),
			frozenEpochs:            1,
			successorFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			sequencerFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("init fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, 968)
		require.NoError(t, err)

		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      td.timestampSlotsForward(1),
			frozenEpochs:            0,
			successorFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			sequencerFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			inflationAdvance:        0,
			advanceShare:            0,
			prntx:                   true,
		})
		// the target pinned a 0 share while the delegator required 968 promille:
		// the floor check rejects before the advance is even compared
		require.NoError(t, util.MustErrorWith(err, "advance share below the required inflation cut"))
	})
	t.Run("frozen epochs 1", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 1
			inflationCut = 10
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			inflationAdvance:        advance,
			advanceShare:            1000,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 1 fail 1", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 1
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1000)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		advance -= 1
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong inflation advance"))
	})
	t.Run("frozen epochs 1 fail 2", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 1
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		require.True(t, advance > 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance())},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance())},
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong frozen coverage value"))
	})
	t.Run("frozen epochs 2", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 2
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)

		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		fc := int64(td.delegatedOutput.Output.TokenBalance() + advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{fc, fc},
			sequencerFrozenCoverage: []int64{fc, fc},
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 2 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 2
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		fc := int64(td.delegatedOutput.Output.TokenBalance() + advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{fc, fc},
			sequencerFrozenCoverage: []int64{fc, fc},
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong inflation advance"))
	})
	t.Run("frozen epochs 3", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 3
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)

		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		covVect := []int64{
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
		}
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: covVect,
			sequencerFrozenCoverage: covVect,
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 3 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 3
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		covVect := []int64{
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
		}
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: covVect,
			sequencerFrozenCoverage: covVect,
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong inflation advance"))
	})
	t.Run("frozen epochs 4", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 4
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		covVect := []int64{
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
		}
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: covVect,
			sequencerFrozenCoverage: covVect,
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 4 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 4
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		covVect := []int64{
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
		}
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: covVect,
			sequencerFrozenCoverage: covVect,
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong inflation advance"))
	})
	t.Run("frozen epochs 8", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 8
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, frozenEpochs, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		covVect := []int64{
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
		}
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: covVect,
			sequencerFrozenCoverage: covVect,
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 8 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs = 8
			inflationCut = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, frozenEpochs, inflationCut)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationCut, advance)
		covVect := []int64{
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
			int64(td.delegatedOutput.Output.TokenBalance() + advance),
		}
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: covVect,
			sequencerFrozenCoverage: covVect,
			inflationAdvance:        advance,
			advanceShare:            100,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong inflation advance"))
	})
}

func TestDelegationUtil(t *testing.T) {
	// All cross-checks exercise the helpers with the library default
	// epochSlots (600 in develop08). Per-target epochSlots is exercised
	// indirectly through the full delegation test paths.
	epochSlots := ledger.L(0).DelegationEpochSlots
	t.Run("offset", func(t *testing.T) {
		const howMany = 100
		for i := 0; i < howMany; i++ {
			chainID := base.RandomChainID()
			direct := ledger.L(0).EpochOffsetSlotsDirect(chainID, epochSlots)
			fromSrc := ledger.L(0).EpochOffsetSlotsFromSource(chainID, epochSlots)
			//t.Logf("fromSrc: %d, direct: %d", fromSrc, direct)
			require.EqualValues(t, fromSrc, direct)
		}
	})
	t.Run("limits", func(t *testing.T) {
		const howMany = 100
		chainID := base.RandomChainID()
		for epoch := uint32(0); epoch < howMany; epoch++ {
			first, last := ledger.L(0).EpochLimits(chainID, epoch, epochSlots)
			//t.Logf("epoch: %d,  first: %d, last: %d, diff: %d", epoch, first, last, last-first)
			if epoch > 0 {
				require.Equal(t, int(last-first), int(epochSlots)-1)
			}
		}
	})
	t.Run("limits+rnd", func(t *testing.T) {
		const howMany = 10000
		var chainID base.ChainID
		for epoch := uint32(0); epoch < howMany; epoch++ {
			direct := ledger.L(0).LastSlotInEpochDirect(chainID, epoch, epochSlots)
			fromSource := ledger.L(0).LastSlotInEpochFromSource(chainID, epoch, epochSlots)
			require.EqualValues(t, fromSource, direct)
			chainID = base.RandomChainID()
		}
	})
	t.Run("epoch from slot 1", func(t *testing.T) {
		const howMany = 10_000
		chainID := base.RandomChainID()
		for slot := uint32(0); slot < howMany; slot++ {
			direct := ledger.L(0).EpochFromSlotDirect(chainID, slot, epochSlots)
			fromSrc := ledger.L(0).EpochFromSlotFromSource(chainID, slot, epochSlots)
			//t.Logf("slot: %d -> epoch: %d", slot, direct)
			require.Equal(t, int(direct), int(fromSrc))
		}
	})
	t.Run("epoch from slot 2", func(t *testing.T) {
		const howMany = 5
		chainID := base.RandomChainID()
		offs := ledger.L(0).EpochOffsetSlotsDirect(chainID, epochSlots)
		t.Logf("offset: %d", offs)
		for epoch := uint32(0); epoch < howMany; epoch++ {
			first, last := ledger.L(0).EpochLimits(chainID, epoch, epochSlots)
			for slot := first; slot <= last; slot++ {
				direct := ledger.L(0).EpochFromSlotDirect(chainID, slot, epochSlots)
				if direct != epoch {
					t.Logf("slot: %d, calc direct epoch: %d, expected: %d", slot, direct, epoch)
				}
				require.Equal(t, int(epoch), int(direct))

			}
		}
	})
	t.Run("epoch from slot 2", func(t *testing.T) {
		const howMany = 1_000_000
		for slot := uint32(0); slot < howMany; slot++ {
			chainID := base.RandomChainID()
			directEpoch := ledger.L(0).EpochFromSlotDirect(chainID, slot, epochSlots)
			first, last := ledger.L(0).EpochLimits(chainID, directEpoch, epochSlots)
			require.True(t, first <= slot && slot <= last)
		}
	})

}

// ==========================================================================
// 2. Attack vectors
// ==========================================================================

// Security-focused delegation constraint tests.
// These tests cover attack vectors and edge cases for the delegateLock EasyFL constraint.
//
// Delegation lock structure (4 constraints required):
//   [0] amount, [1] delegateLock, [2] chain, [3] delegateLockState
//
// Two unlock modes:
//   - Master unlock: byte(selfUnlockParameters,2) == 0xff, requires sigLock(masterID), not frozen
//   - Target unlock: byte(selfUnlockParameters,2) != 0xff, requires chainLock, not on-hold,
//     amount cannot decrease, lock must be identical on successor, cannot discontinue chain
//
// Delegation states: undef (0), frozen (1), on_hold (2)

// delegTestEnv holds test environment for delegation security tests.
type delegTestEnv struct {
	u                *utxodb.UTXODB
	masterPrivateKey ed25519.PrivateKey
	masterAddr       ledger.SigLock
	seqPrivateKey    ed25519.PrivateKey
	seqAddr          ledger.SigLock
	target           base.ChainID
	seqChainOrigin   ledger.OutputWithChainID
	delegatedOutput  ledger.DelegationOutput
}

const (
	cdelegInitAmount     = 200_000_000_000
	cdelegOnChainBalance = 3_000_000_000
	cdelegTokens         = 1_000_000_000
)

// setupDelegEnv creates a sequencer chain and a delegation output targeting it.
func setupDelegEnv(t *testing.T, maxFrozenEpochs byte, inflationCut uint16) *delegTestEnv {
	t.Helper()
	env := &delegTestEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := env.u.GenerateAddresses(0, 2)
	env.masterPrivateKey = privKey[0]
	env.masterAddr = addr[0]
	env.seqPrivateKey = privKey[1]
	env.seqAddr = addr[1]

	err := env.u.TokensFromFaucet(env.masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = env.u.TokensFromFaucet(env.seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// Sequencer chain origin must be created via a proper sequencer tx
	// (sequencer constraint at slot 4 requires SetSequencerData + endorsement).
	env.seqChainOrigin = mustMakeSequencerChainOrigin(t, env.u, env.seqPrivateKey, env.seqAddr, cdelegOnChainBalance)
	env.target = env.seqChainOrigin.ChainID

	// create delegation output
	masterOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := env.seqChainOrigin.Timestamp().AddSlots(1)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	delegOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:               cdelegTokens,
		MasterID:             base.HolderID(env.masterAddr),
		Target:               env.target,
		RequiredInflationCut: inflationCut,
		StartSlot:            delegTs.Slot,
	})
	_, err = txb.ProduceOutput(delegOut)
	require.NoError(t, err)
	remainder := masterOuts[0].Output.TokenBalance() - cdelegTokens
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(remainder).WithLock(env.masterAddr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(delegTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.masterPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	// retrieve delegation output
	delegOuts, err := env.u.SugaredStateReader().GetOutputsDelegatedToAccount2(env.target[:])
	require.NoError(t, err)
	require.EqualValues(t, 1, len(delegOuts))
	env.delegatedOutput, _ = ledger.DelegationOutputFromOutputWithChainID(delegOuts[0])

	return env
}

// freezeDelegation transitions the delegation to frozen state by the target.
// Returns the updated env with fresh chain tip and delegation tip.
func (env *delegTestEnv) freezeDelegation(t *testing.T, frozenEpochs byte) {
	t.Helper()
	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	freezeUntilEpoch := env.delegatedOutput.FreezeUntilMax(ts)
	requiredAdvance, err := env.delegatedOutput.RequiredMinimumInflationAdvance(ts, freezeUntilEpoch)
	require.NoError(t, err)

	delegSuccessor, err := env.delegatedOutput.MakeDelegationFreezeOutput(ts, freezeUntilEpoch, 1, env.delegatedOutput.RequiredInflationCut, true)
	require.NoError(t, err)

	txb := exhelp.New()
	_, _, err = txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	seqChainIdx, err := txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance() - requiredAdvance))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	_, err = txb.ProduceOutput(delegSuccessor)
	require.NoError(t, err)

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(t, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, ts)

	// Sequencer chain successor at seqChainIdx requires sequencer tx
	// setup (SetSequencerData + cross-slot endorsement).
	txb.SetSequencerData(seqChainIdx, txbuildercore.SequencerOutputIndexNone)
	dummyTxId := base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(env.seqPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	// refresh tips
	env.delegatedOutput, err = env.u.SugaredStateReader().GetDelegatedOutput(env.delegatedOutput.ChainID)
	require.NoError(t, err)
	env.seqChainOrigin, err = env.u.SugaredStateReader().GetChainOutputWithChainID(env.seqChainOrigin.ChainID)
	require.NoError(t, err)
}

// TestClaudeDelegationWrongMasterUnlock verifies that a third party (not the master)
// cannot unlock a delegation output using master unlock mode (byte 2 = 0xff).
// The sigLock($1) check in _masterUnlockedConsumed requires the signer to match masterID.
func TestClaudeDelegationWrongMasterUnlock(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	// attacker (seq controller) tries to unlock as master
	txb := exhelp.New()
	amount, _, err := txb.ConsumeOutputsNoUnlock(&env.delegatedOutput.OutputWithID)
	require.NoError(t, err)

	// mark as master unlock (byte 2 = 0xff)
	txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0xff, 0xff, 0xff})
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(env.seqAddr)
	}))
	require.NoError(t, err)

	ts := env.delegatedOutput.Timestamp().AddSlots(1)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	// sign with seq controller key, NOT master key
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "wrong master should not unlock delegation")
}

// TestClaudeDelegationTargetReducesAmount verifies that the target sequencer
// cannot reduce the delegated amount on the successor output.
// EasyFL: lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor)
func TestClaudeDelegationTargetReducesAmount(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	// sequencer takes stolen tokens
	stolenAmount := uint64(100_000_000)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance() + stolenAmount))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// produce delegation successor with reduced amount
	reducedAmount := env.delegatedOutput.Output.TokenBalance() - stolenAmount
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(reducedAmount))
		o.WithLock(env.delegatedOutput.Output.Lock())
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "target should not be able to reduce delegated amount")
	require.NoError(t, util.MustErrorWith(err, "delegated amount should not decrease"))
}

// TestClaudeDelegationTargetChangesLock verifies that the target sequencer cannot
// modify the immutable delegation lock parameters on the successor output.
// EasyFL: equal(successorConstraint(1), selfSiblingConstraint(lockConstraintIndex))
func TestClaudeDelegationTargetChangesLock(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// produce delegation successor with MODIFIED lock (different master)
	attackerMasterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.seqPrivateKey))
	tamperedLock := ledger.NewDelegateLock(env.target, attackerMasterID, 0)
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
		o.WithLock(tamperedLock)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "target should not be able to change delegation lock")
	require.NoError(t, util.MustErrorWith(err, "delegation index values on successor must be exactly the same"))
}

// TestClaudeDelegationTargetDiscontinuesChain verifies that the target sequencer
// cannot terminate (discontinue) a delegation chain. Only the master can do that.
// EasyFL: not(equal(selfSiblingUnlockParams(2),0xffff)) -> target_cannot_discontinue
func TestClaudeDelegationTargetDiscontinuesChain(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance() + env.delegatedOutput.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	// target unlock (byte 2 = 0) but with chain termination unlock params
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "target should not be able to discontinue delegation chain")
	require.NoError(t, util.MustErrorWith(err, "target cannot discontinue the delegation chain"))
}

// TestClaudeDelegationOriginCannotBeFrozen verifies that a delegation origin
// output cannot be created in frozen state.
// EasyFL: not(_selfIsDelegationOrigin) inside _validLimitsProducedFrozen
func TestClaudeDelegationOriginCannotBeFrozen(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddresses(0, 2)
	masterPrivateKey := privKey[0]
	masterAddr := addr[0]
	seqPrivateKey := privKey[1]
	seqAddr := addr[1]

	err := u.TokensFromFaucet(masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain
	seqOuts, err := u.SugaredStateReader().GetOutputsForAccount(seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	target := chOuts[0].ChainID

	// try to create delegation origin in FROZEN state
	masterOuts, err := u.SugaredStateReader().GetOutputsForAccount(masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := chOuts[0].Timestamp().AddSlots(1)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// manually build delegation origin with frozen state (bypassing helper)
	delegLock := ledger.NewDelegateLock(target, base.HolderID(masterAddr), 0)
	frozenOrigin := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(cdelegTokens))
		o.WithLock(delegLock)
		o.MustPushConstraint(ledger.NewChainOrigin(delegTs.Slot).Bytes())
		// frozen state at origin - should be rejected
		o.MustPushConstraint(ledger.DelegateLockState{LastFrozenEpoch: 5, State: ledger.DelegateLockStateFrozen}.Bytes())
	})
	_, err = txb.ProduceOutput(frozenOrigin)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - cdelegTokens).WithLock(masterAddr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(delegTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(masterPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	// The EasyFL checks in _validLimitsProducedFrozen fire in order:
	// 1. last_frozen_epoch_cannot_be_in_the_past
	// 2. frozen_epochs_cannot_exceed_maximum_set_by_delegator
	// 3. delegation_origin_cannot_be_frozen
	// Which check fires first depends on the epoch values. The key assertion
	// is that a frozen delegation origin is rejected regardless.
	require.Error(t, err, "delegation origin should not be created frozen")
}

// TestClaudeDelegationWrongConstraintCount verifies that a delegation
// output with junk appended after the delegateLockState is rejected.
// Per Option C of claude/archive/shipped/delegation_epoch_params.md the state must
// occupy the LAST tuple position; pushing more constraints after it
// makes the state no longer last AND breaks the "last position parses
// as delegateLockState" structure check.
func TestClaudeDelegationWrongConstraintCount(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddresses(0, 2)
	masterPrivateKey := privKey[0]
	masterAddr := addr[0]
	seqPrivateKey := privKey[1]
	seqAddr := addr[1]

	err := u.TokensFromFaucet(masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain
	seqOuts, err := u.SugaredStateReader().GetOutputsForAccount(seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	target := chOuts[0].ChainID

	// try to create delegation output with 5 constraints (extra injected constraint)
	masterOuts, err := u.SugaredStateReader().GetOutputsForAccount(masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := chOuts[0].Timestamp().AddSlots(1)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	delegLock := ledger.NewDelegateLock(target, base.HolderID(masterAddr), 0)
	// build delegation with extra constraint (5 total)
	delegWithExtra := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(cdelegTokens))
		o.WithLock(delegLock)
		o.MustPushConstraint(ledger.NewChainOrigin(delegTs.Slot).Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		// extra constraint - should be rejected
		o.MustPushConstraint(ledger.NewAmounts(int64(cdelegTokens)).Bytes())
	})
	_, err = txb.ProduceOutput(delegWithExtra)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - cdelegTokens).WithLock(masterAddr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(delegTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(masterPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "delegation with 5 constraints should be rejected")
	// Per Option C the structure check on the delegateLock side reads
	// the last tuple position via parseBytecode(..., #delegateLockState).
	// Junk at the last position panics parseBytecode before any explicit
	// `require` can fire, so the surfaced error is the panic from the
	// EasyFL VM — sufficient to confirm the tx was rejected.
	require.NoError(t, util.MustErrorWith(err, "wrong function code"))
}

// TestClaudeDelegationSafeRevocationWindow verifies that the target sequencer
// cannot unlock a frozen delegation during the safe revocation window.
// The safe revocation window starts after the last frozen slot and lasts
// constDelegationSafeRevocationSlots (60 slots = 10 min).
// This protects the master's ability to reclaim during this window.
func TestClaudeDelegationSafeRevocationWindow(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	// freeze the delegation
	env.freezeDelegation(t, 1)
	require.True(t, env.delegatedOutput.IsMarkedFrozen(), "should be frozen after freeze")

	unfreezeSlot := env.delegatedOutput.UnfreezeSlot()
	safeRevSlots := ledger.L(0).SafeRevocationSlots

	// target tries to consume in safe revocation window
	t.Run("target blocked in safe revocation window", func(t *testing.T) {
		// use a slot right in the middle of safe revocation window
		attackTs := base.T(unfreezeSlot+safeRevSlots/2, 5)

		txb := exhelp.New()
		_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
		require.NoError(t, err)

		successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
		_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
			o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
		}))
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
		require.NoError(t, err)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

		// produce valid delegation successor
		cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
			o.WithLock(env.delegatedOutput.Output.Lock())
			o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
			o.MustPushConstraint(ledger.DelegateLockState{State: ledger.DelegateLockStateOnHold}.Bytes())
		}))
		require.NoError(t, err)

		txb.ComputeInputCommitment()
		txb.SetTimestamp(attackTs)
		txb.SignED25519(env.seqPrivateKey)
		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.Error(t, err, "target should not unlock during safe revocation window")
		require.NoError(t, util.MustErrorWith(err, "delegation cannot be unlocked by the target in safe revocation window"))
	})

	// master CAN unlock after freeze expires (not in safe revocation window)
	t.Run("master can unlock after freeze expires", func(t *testing.T) {
		// slot after safe revocation window ends
		masterTs := base.T(unfreezeSlot+safeRevSlots+10, 5)

		txb := exhelp.New()
		amount, _, err := txb.ConsumeOutputsNoUnlock(&env.delegatedOutput.OutputWithID)
		require.NoError(t, err)

		txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0xff, 0xff})
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amount)).WithLock(env.masterAddr)
		}))
		require.NoError(t, err)

		txb.SetTimestamp(masterTs)
		txb.ComputeInputCommitment()
		txb.SignED25519(env.masterPrivateKey)
		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.NoError(t, err, "master should unlock after safe revocation window")
	})
}

// TestClaudeDelegationInflationCutAbove1000 verifies that creating a delegation
// with requiredInflationCut > 1000 (promille) is rejected.
// EasyFL: lessOrEqualThan($1, u64/1000)
func TestClaudeDelegationInflationCutAbove1000(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddresses(0, 2)
	masterPrivateKey := privKey[0]
	masterAddr := addr[0]
	seqPrivateKey := privKey[1]
	seqAddr := addr[1]

	err := u.TokensFromFaucet(masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain
	seqOuts, err := u.SugaredStateReader().GetOutputsForAccount(seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	target := chOuts[0].ChainID

	// create delegation with inflation cut = 1001 (above max 1000)
	masterOuts, err := u.SugaredStateReader().GetOutputsForAccount(masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := chOuts[0].Timestamp().AddSlots(1)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	delegOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:               cdelegTokens,
		MasterID:             base.HolderID(masterAddr),
		Target:               target,
		RequiredInflationCut: 1001, // above max
		StartSlot:            delegTs.Slot,
	})
	_, err = txb.ProduceOutput(delegOut)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - cdelegTokens).WithLock(masterAddr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(delegTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(masterPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "inflation cut > 1000 should be rejected")
	require.NoError(t, util.MustErrorWith(err, "max required inflation cut must be in promille less or equal than 1000"))
}

// TestClaudeDelegationOnHoldTargetRelock verifies that once a delegation
// is put on hold (revoked), the target cannot re-freeze it.
// EasyFL: not(_selfIsMarkedOnHold) in _requireUnlockableByTheTarget
func TestClaudeDelegationOnHoldTargetRelock(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	// freeze, then revoke
	env.freezeDelegation(t, 1)
	require.True(t, env.delegatedOutput.IsMarkedFrozen())

	// now revoke: target puts on hold
	unfreezeSlot := env.delegatedOutput.UnfreezeSlot()
	revokeTs := base.T(unfreezeSlot-10, 5) // inside freeze but before unfreeze
	// for revocation inside freeze, target can only put on hold

	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	seqChainIdx, err := txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	delegatedOutPar := ledger.MakeDelegationRevokeOutputParams{
		TxTs:                     revokeTs,
		Inflation:                0,
		HarvestInflation:         0,
		DisableConsistencyChecks: true,
	}
	delegatedOutPar.PredOutputIndex, err = txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	delegatedOut, err := env.delegatedOutput.MakeDelegationRevokeOutput(delegatedOutPar)
	require.NoError(t, err)

	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	_, err = txb.ProduceOutput(delegatedOut)
	require.NoError(t, err)

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(t, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, revokeTs)

	// Sequencer chain transit requires sequencer tx + endorsement.
	txb.SetSequencerData(seqChainIdx, txbuildercore.SequencerOutputIndexNone)
	dummyTxId := base.NewTransactionID(revokeTs.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(revokeTs)
	txb.SignED25519(env.seqPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	// refresh
	env.delegatedOutput, err = env.u.SugaredStateReader().GetDelegatedOutput(env.delegatedOutput.ChainID)
	require.NoError(t, err)
	env.seqChainOrigin, err = env.u.SugaredStateReader().GetChainOutputWithChainID(env.seqChainOrigin.ChainID)
	require.NoError(t, err)

	require.True(t, env.delegatedOutput.IsMarkedOnHold(), "should be on hold after revocation")

	// now target tries to re-freeze the on-hold delegation
	relockTs := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb2 := exhelp.New()
	_, _, err = txb2.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint2 := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb2.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint2.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb2.PutSignatureUnlock(0)
	txb2.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx2, err := txb2.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb2.PutUnlockParams(predIdx2, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb2.PutUnlockParams(predIdx2, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// try to produce frozen successor from on-hold
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx2, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb2.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
		o.WithLock(env.delegatedOutput.Output.Lock())
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{LastFrozenEpoch: 5, State: ledger.DelegateLockStateFrozen}.Bytes())
	}))
	require.NoError(t, err)

	txb2.ComputeInputCommitment()
	txb2.SetTimestamp(relockTs)
	txb2.SignED25519(env.seqPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb2)
	require.Error(t, err, "target should not re-freeze on-hold delegation")
	require.NoError(t, util.MustErrorWith(err, "on hold delegation cannot be unlocked by the target"))
}

// ==========================================================================
// 3. Epoch parameters and foundry delegation
// ==========================================================================

// --------------------------------------------------------------------------
// Foundry-delegation: the canonical Option C scenario
// --------------------------------------------------------------------------

// foundryDelegationEnv extends the plain delegation test environment
// with a separately-owned foundry chain. The foundry's controller is
// the master that will delegate the foundry chain to the sequencer.
type foundryDelegationEnv struct {
	td      *testData
	foundry base.ChainID
}

// newFoundryDelegationEnv sets up a sequencer chain (the delegation
// target, accepting delegations via delegationParams at index 6) and a
// foundry chain owned by the master account. policy is optional: when
// non-nil it goes at ConstraintIndexFoundryPolicy on the foundry origin
// — typically foundryNonDestructible to exercise the canonical
// "non-destructible foundry, then delegate it" scenario.
func newFoundryDelegationEnv(t *testing.T, policy []byte) *foundryDelegationEnv {
	td := &testData{T: t}
	td.init() // also creates the sequencer chain with delegationParams

	// Create a foundry chain owned by the master (the "delegator-to-be").
	outs := getSourceOutputs(t, td.u, td.masterAddr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	txb := exhelp.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	const foundryOnChain = uint64(500_000_000)
	foundryOut := exhelp.MakeFoundryOriginOutput(foundryOnChain, td.masterAddr, ts.Slot, 0, policy)
	require.NoError(t, foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, td.masterAddr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)
	txBytes, txid, failedTx, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "foundry origin build failed: %s", failedTx)
	require.NoError(t, td.u.AddTransaction(txBytes))

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(foundryOid)

	// Settle the foundry tag: do a zero-supply transit so the
	// foundry constraint's tag flips from NilChainID at origin to the
	// real chain ID. The foundryPolicy at index 5 (if any) survives
	// byte-equal across this transit, exercising the policy's
	// selfImmutableOnSuccessorIndex(5) check.
	{
		settleTxb := exhelp.New()
		fIn := &ledger.OutputDataWithChainID{
			OutputDataWithID: ledger.OutputDataWithID{ID: foundryOid, Data: foundryOut.Bytes()},
			ChainID:          chainID,
		}
		_, err = settleTxb.TransitFoundry(fIn, 0)
		require.NoError(t, err)
		settleTxb.PutSignatureUnlock(0)
		// pure base-token funding for the storage deposit on the produced
		// foundry chain output (its size unchanged, so the existing
		// 500_000_000 balance keeps covering the deposit, no
		// additional funding strictly required).
		settleTs := ts.AddTicks(int(ledger.L(0).TransactionPace))
		settleTxb.SetTimestamp(settleTs)
		settleTxb.ComputeInputCommitment()
		settleTxb.SignED25519(td.masterPrivateKey)
		settleBytes, _, failed, err := txbtest.BuildAndValidate(settleTxb)
		require.NoError(t, err, "foundry tag-settle transit failed: %s", failed)
		require.NoError(t, td.u.AddTransaction(settleBytes))
	}

	return &foundryDelegationEnv{
		td:      td,
		foundry: chainID,
	}
}

// delegateFoundryChain transits the foundry chain to a delegation
// pointing at the sequencer target. The transit:
//   - replaces sigLock at index 2 with delegateLock(master=master,
//     target=sequencer)
//   - preserves the foundry constraint at index 4 byte-equal
//   - preserves the foundryPolicy at index 5 byte-equal (if attached)
//   - appends delegateLockState at the last tuple position
//
// Returns the transit tx error.
func (e *foundryDelegationEnv) delegateFoundryChain(t *testing.T) error {
	t.Helper()
	td := e.td

	// Fetch the current foundry chain output.
	chData, err := td.u.StateReader().GetUTXOForChainID(e.foundry)
	require.NoError(t, err)
	chParsed, err := chData.Parse()
	require.NoError(t, err)
	chIn, ok := ledger.AsOutputWithChainID(chParsed.Output, chParsed.ID)
	require.True(t, ok)

	// Build the delegation lock with the target's inlined params.
	lib := ledger.L(0)
	delLock := ledger.NewDelegateLock(
		td.target,
		base.HolderID(td.masterAddr),
		900, // 90% inflation cut
	)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chIn.Output, chIn.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, predIdx)
	txb.PutSignatureUnlock(0)
	ts := chIn.Timestamp().AddTicks(int(lib.TransactionPace))

	// Build the successor by cloning the existing foundry chain output
	// and overlaying the new lock at index 2 + new chain constraint at
	// index 3 + appended delegateLockState at the last position. Clone
	// preserves the foundry at index 4 and foundryPolicy at index 5 (if
	// any) byte-equal — which is what foundryNonDestructible /
	// foundryMaxSupply's selfImmutableOnSuccessorIndex(5) requires.
	successorCC := ledger.NewChainConstraint(
		chIn.ChainID, predIdx, chIn.OriginSlot,
		chIn.CumulativeChainInflation, chIn.CumulativeBranchBonus,
		chIn.TransitionCounter+1, chIn.BranchCounter,
	)
	succ := chIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithLock(delLock)
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
		// Append delegateLockState at the last position. The output
		// builder picks the next available index after the existing
		// constraints; for foundry-no-policy that's 5, for foundry-with-
		// policy that's 6. Either way it lands at NumElements - 1, which
		// is what Option C requires.
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, failedTx, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		t.Logf("foundry-delegate build failed:\n%s", failedTx)
		return err
	}
	return td.u.AddTransaction(txBytes)
}

// TestDelegateFoundryChainNoPolicy delegates a plain foundry (no policy
// at index 5) to the sequencer target. The produced output has
// delegateLock at 2, chain at 3, foundry at 4, delegateLockState at 5 —
// delegateLockState is the last position, satisfying Option C.
func TestDelegateFoundryChainNoPolicy(t *testing.T) {
	e := newFoundryDelegationEnv(t, nil /* no policy */)
	require.NoError(t, e.delegateFoundryChain(t))

	// Re-read the delegated output and verify its shape.
	delOut, err := e.td.u.SugaredStateReader().GetChainOutputWithChainID(e.foundry)
	require.NoError(t, err)
	require.EqualValues(t, ledger.DelegateLockName, delOut.Output.Lock().Name(),
		"chain output should now be a delegation")
	// Foundry preserved at index 4.
	fBytes, err := delOut.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	require.NoError(t, err)
	_, err = ledger.FoundryFromBytes(fBytes)
	require.NoError(t, err, "foundry constraint at index 4 preserved")
	// delegateLockState at the last index.
	n := delOut.Output.NumElements()
	stateBytes, err := delOut.Output.ConstraintAt(byte(n - 1))
	require.NoError(t, err)
	_, err = ledger.DelegateLockStateFromBytesWithLib(stateBytes, ledger.L(0))
	require.NoError(t, err, "last constraint is delegateLockState")
	// Concretely: amounts (0), index-values (1), delegateLock (2),
	// chain (3), foundry (4), state (5) → 6 elements.
	require.EqualValues(t, 6, n)
}

// TestDelegateFoundryChainNonDestructible is the canonical Option C
// scenario: a foundryNonDestructible foundry chain is delegated to a
// sequencer. The foundry's policy at index 5 self-locks via
// selfImmutableOnSuccessorIndex(5); the transit preserves it byte-equal
// so the policy still passes, and the delegateLockState lands at index
// 6 (= last).
func TestDelegateFoundryChainNonDestructible(t *testing.T) {
	policy := ledger.FoundryNonDestructibleBytecode()
	e := newFoundryDelegationEnv(t, policy)
	require.NoError(t, e.delegateFoundryChain(t))

	delOut, err := e.td.u.SugaredStateReader().GetChainOutputWithChainID(e.foundry)
	require.NoError(t, err)
	require.EqualValues(t, ledger.DelegateLockName, delOut.Output.Lock().Name())

	// Foundry constraint preserved at 4, foundryPolicy preserved at 5,
	// delegateLockState appended at 6 (= NumElements - 1).
	fBytes, err := delOut.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	require.NoError(t, err)
	_, err = ledger.FoundryFromBytes(fBytes)
	require.NoError(t, err)

	gotPolicy, err := delOut.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy)
	require.NoError(t, err)
	require.Equal(t, policy, gotPolicy, "foundryNonDestructible policy preserved byte-equal across transit")

	n := delOut.Output.NumElements()
	require.EqualValues(t, 7, n,
		"layout: amounts, iv, delegateLock, chain, foundry, foundryPolicy, delegateLockState")
	stateBytes, err := delOut.Output.ConstraintAt(byte(n - 1))
	require.NoError(t, err)
	_, err = ledger.DelegateLockStateFromBytesWithLib(stateBytes, ledger.L(0))
	require.NoError(t, err)
}

// TestDelegateLockStateMustBeLast injects an extra constraint after the
// delegateLockState; the state's own "I must be at the last index"
// check (Option C) refuses. The structural check inside the
// delegateLock body also panics via parseBytecode (the last-position
// bytecode isn't a delegateLockState). Either path is acceptable as
// long as the tx is rejected; we match on a stable substring shared by
// every related failure path.
func TestDelegateLockStateMustBeLast(t *testing.T) {
	td := &testData{T: t}
	td.init()

	// Build a delegation origin output and try to append junk after the
	// state. We use the existing delegationOriginDirect builder via a
	// manual tx so we can inject the extra constraint.
	ts := td.seqChainOrigin.Timestamp().AddTicks(1)
	masterOuts, _ := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(
		td.masterAddr, delegatedTokens+1_000)
	require.True(t, len(masterOuts) > 0)

	delLock := ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 0)

	txb := exhelp.New()
	idx, err := txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, idx)
	txb.PutSignatureUnlock(0)

	delOriginOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(delegatedTokens))
		o.WithLock(delLock)
		o.MustPushConstraint(ledger.NewChainOrigin(ts.Slot).Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		// Junk after the state — would put delegateLockState at index 4
		// while NumElements is 6 → state's "must be last" check fails,
		// and the parseBytecode lookup in _validStructureProduced panics
		// on the non-delegateLockState bytecode at index 5.
		o.MustPushConstraint(ledger.NewAmounts(int64(123)).Bytes())
	})
	_, err = txb.ProduceOutput(delOriginOut)
	require.NoError(t, err)
	// Remainder back to master so the tx balances.
	remainder := masterOuts[0].Output.TokenBalance() - delegatedTokens
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(remainder).WithLock(td.masterAddr)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)
	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "delegation with junk after delegateLockState must be rejected")
}

// --------------------------------------------------------------------------
// Once delegated: what the target may do to the foundry
//
// The delegator earns inflation on the tokens sitting on the foundry chain,
// and that is all the target is there for. It is not the foundry's
// controller, so it may neither move the supply — it could otherwise push a
// token(...) declaration of its own like any builder — nor attach anything
// to the chain, since a policy script self-locks and would be permanent.
// Both fall out of the target-scope rule in section 5; the foundry's own
// rules are in foundry_test.go.
// --------------------------------------------------------------------------

// delegatedTransitOpts describes what the delegation TARGET attempts to do
// to the delegated foundry chain while transiting it.
type delegatedTransitOpts struct {
	// mint, when > 0, raises the supply by that much, produces a
	// tokenAmount to the sequencer controller's own address, pushes the
	// token(...) declaration and points the foundry at it.
	mint uint64
	// injectPolicy, when non-nil, is placed at ConstraintIndexFoundryPolicy
	// on the successor, moving the delegateLockState one slot along.
	injectPolicy []byte
}

// targetTransit builds the sequencer-side transit of the delegated foundry:
// the target's own chain at input 0 (unlocking the delegation), the delegated
// foundry at input 1, and the canonical delegation successor amounts / chain
// / state, modified per opt. Returns the validation/submission error.
func (e *foundryDelegationEnv) targetTransit(t *testing.T, opt delegatedTransitOpts) error {
	t.Helper()
	td := e.td

	delOut, err := td.u.SugaredStateReader().GetDelegatedOutput(e.foundry)
	require.NoError(t, err)
	fIn, err := ledger.FoundryFromBytes(mustConstraintAt(t, delOut.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)

	ts := td.timestampSlotsForward(1)
	requiredAdvance, err := delOut.RequiredMinimumInflationAdvance(ts, 0)
	require.NoError(t, err)
	// The canonical successor the target would build: correct amounts,
	// chain constraint and delegateLockState. It carries no foundry (the
	// wallet helper builds a plain delegation), so we take those three
	// elements from it and keep everything else from the consumed output.
	canon, err := delOut.MakeDelegationFreezeOutput(ts, 0, 1, delOut.RequiredInflationCut, true)
	require.NoError(t, err)
	const canonStateIndex = byte(4)

	txb := exhelp.New()
	// Input 0: the target's own (sequencer) chain — unlocking it is what
	// authorises the delegation's target path.
	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(t, err)
	seqCC := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot,
		0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	seqSpend := requiredAdvance
	if opt.mint > 0 {
		seqSpend += 100_000_000 // storage deposit for the minted output
	}
	seqChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance() - seqSpend))
		o.PutConstraint(seqCC.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	// Input 1: the delegated foundry, unlocked on the target path
	// (second unlock byte != 0xff).
	predIdx, err := txb.ConsumeOutput(delOut.Output, delOut.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	stateIndex := byte(ledger.ConstraintIndexFoundryPolicy) // 5: where the state sits today
	if opt.injectPolicy != nil {
		stateIndex++ // the injected policy takes slot 5, the state moves to 6
	}
	succ := delOut.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(mustConstraintAt(t, canon, ledger.ConstraintIndexAmounts), ledger.ConstraintIndexAmounts)
		o.PutConstraint(mustConstraintAt(t, canon, ledger.ConstraintIndexChain), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(fIn.Supply+opt.mint).Bytes(), ledger.ConstraintIndexFoundry)
		if opt.injectPolicy != nil {
			o.PutConstraint(opt.injectPolicy, ledger.ConstraintIndexFoundryPolicy)
		}
		o.PutConstraint(mustConstraintAt(t, canon, canonStateIndex), stateIndex)
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)

	if opt.mint > 0 {
		_, _, addrs := td.u.GenerateAddresses(0, 2)
		minted := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(100_000_000).WithLock(addrs[1]). // the sequencer controller's own address
										WithTokenAmount(e.foundry, opt.mint)
		})
		require.NoError(t, minted.EnoughAmountForStorageDeposit())
		_, err = txb.ProduceOutput(minted)
		require.NoError(t, err)

		declIdx := byte(len(txb.TxData.TxConstraints))
		txb.PushTxConstraint(ledger.TokenFoundryBytecode(e.foundry, succIdx))
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexFoundry, []byte{declIdx})
	}

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(t, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, ts)

	// The target's chain carries the sequencer constraint, so the tx must be
	// a sequencer tx; the dummy endorsement satisfies the cross-slot case.
	txb.SetSequencerData(seqChainIdx, txbuildercore.SequencerOutputIndexNone)
	txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))

	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, failedTx, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		t.Logf("target transit rejected:\n%s", failedTx)
		return err
	}
	return td.u.AddTransaction(txBytes)
}

// The plain target transit — the delegation's whole purpose — must stay
// valid: the target moves amounts, chain and state, and leaves the foundry
// alone.
func TestDelegatedFoundryTargetTransitAccepted(t *testing.T) {
	e := newFoundryDelegationEnv(t, nil)
	require.NoError(t, e.delegateFoundryChain(t))

	require.NoError(t, e.targetTransit(t, delegatedTransitOpts{}),
		"a delegation target must be able to transit a delegated foundry chain untouched")
}

// The target holds no minting authority: it may not raise the supply, even
// with a correctly-formed declaration of its own, and even though the tx
// balances (the minted tokenAmount matches the delta).
func TestDelegatedFoundryTargetCannotMint(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryDelegationEnv(t, nil)
	require.NoError(t, e.delegateFoundryChain(t))

	err := e.targetTransit(t, delegatedTransitOpts{mint: mintAmount})
	require.Error(t, err, "the delegation target must not be able to mint the delegated foundry's token")
	require.NoError(t, util.MustErrorWith(err, "delegation target cannot modify constraints"))
}

// A policy script self-locks once it is on the chain, so a target able to
// attach one could permanently cripple the foundry — foundryMaxSupply(0)
// blocks every future mint. The target may not add constraints at all.
func TestDelegatedFoundryTargetCannotInjectPolicy(t *testing.T) {
	e := newFoundryDelegationEnv(t, nil)
	require.NoError(t, e.delegateFoundryChain(t))

	err := e.targetTransit(t, delegatedTransitOpts{injectPolicy: ledger.FoundryMaxSupplyBytecode(0)})
	require.Error(t, err, "the delegation target must not be able to attach a policy to the delegated foundry")
	require.NoError(t, util.MustErrorWith(err, "delegation target cannot add or remove constraints"))
}

// The master keeps its minting authority while the chain stays delegated:
// unlocking on the master path (second unlock byte 0xff) it mints and leaves
// the delegation in place.
func TestDelegatedFoundryMasterCanMint(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryDelegationEnv(t, nil)
	require.NoError(t, e.delegateFoundryChain(t))
	td := e.td

	delOut, err := td.u.SugaredStateReader().GetDelegatedOutput(e.foundry)
	require.NoError(t, err)
	fIn, err := ledger.FoundryFromBytes(mustConstraintAt(t, delOut.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(delOut.Output, delOut.ID)
	require.NoError(t, err)
	// Master unlock: 0xff in the second byte; the first unlocks the master's
	// own sigLock via the transaction signature.
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, []byte{0xff, 0xff})

	cc := delOut.Output.ChainConstraint()
	require.NotNil(t, cc)
	succCC := ledger.NewChainConstraint(
		e.foundry, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	succ := delOut.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(succCC.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(fIn.Supply+mintAmount).Bytes(), ledger.ConstraintIndexFoundry)
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	// Funding for the minted output's storage deposit, from the master's wallet.
	masterOuts := getSourceOutputs(t, td.u, td.masterAddr)
	fundIdx, err := txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(fundIdx)

	minted := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(100_000_000).WithLock(td.masterAddr).WithTokenAmount(e.foundry, mintAmount)
	})
	require.NoError(t, minted.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(minted)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, td.masterAddr)

	declIdx := byte(len(txb.TxData.TxConstraints))
	txb.PushTxConstraint(ledger.TokenFoundryBytecode(e.foundry, succIdx))
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexFoundry, []byte{declIdx})

	ts := base.MaximumTime(delOut.ID.Timestamp(), masterOuts[0].ID.Timestamp()).AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)

	txBytes, _, failedTx, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "the delegation master must keep its minting authority: %s", failedTx)
	require.NoError(t, td.u.AddTransaction(txBytes))
}

// ==========================================================================
// 4. Allowance on the askstop request
// ==========================================================================

// Tests for the delegation allowance: an ensureStopDelegation argument that
// lets the target sequencer charge the askstop compensation to the
// delegation balance instead of to the delegator's own tokens. The
// delegateLock's non-decrease gate is relaxed by exactly that amount when a
// third unlock byte points at the consumed request output.

const allowanceRequestFee = 1_000

// makeAllowanceRequestOutput produces, in its own transaction signed by
// `signer`, the tag-along command output carrying ensureStopDelegation at
// element 4. It has to be a separate transaction because tagAlong pins its
// senderID to the signer of the transaction that produced it — that binding
// is what makes the allowance an authorisation rather than a self-asserted
// field, so the tests must go through it rather than hand-building the
// output inside the revoke transaction.
func (td *testData) makeAllowanceRequestOutput(
	ts base.LedgerTime,
	signer ed25519.PrivateKey,
	sender ledger.SigLock,
	namedDelegation base.ChainID,
	allowance uint64,
) *ledger.OutputWithID {
	par, err := td.u.MakeTransferInputData(signer, nil, ts)
	require.NoError(td, err)

	reqParams := smallkv.New()
	reqParams.Set(byte(0), []byte{3}) // FieldCmdCode = RequestCodeAskStopDelegation
	reqParams.Set('i', namedDelegation[:])

	txBytes, err := utxodb.MakeSimpleTransferTransaction(
		par.WithAmount(allowanceRequestFee).
			WithTargetLock(&ledger.TagAlongLock{
				TargetSequencerID: td.target,
				SenderID:          base.HolderID(sender),
			}).
			// appended in order: request data lands at element 3, the
			// ensureStopDelegation at 4, which is where the delegate lock
			// looks for the allowance
			WithConstraintBinary(easyfl.InlineDataBytecode(reqParams.Bytes())).
			WithConstraint(&ledger.EnsureStopDelegation{ChainID: namedDelegation, Allowance: allowance}),
	)
	require.NoError(td, err)
	require.NoError(td, td.u.AddTransaction(txBytes))

	// locate the produced tag-along output in the sequencer's backlog
	outs, err := td.u.SugaredStateReader().GetTagAlongBacklogForSequencer(td.target)
	require.NoError(td, err)
	for i := range outs {
		if _, idx := outs[i].Output.EnsureStopDelegationConstraint(); idx == 4 {
			ret := outs[i]
			return &ret
		}
	}
	require.Fail(td, "allowance request output not found")
	return nil
}

type allowanceRevokeParams struct {
	ts base.LedgerTime
	// request output carrying the allowance; nil means no allowance at all
	request *ledger.OutputWithID
	// how much to actually take out of the delegation balance
	take uint64
	// omit the third unlock byte even though a request output is consumed
	omitUnlockRef bool
	// unlock the delegation as master rather than target
	unlockAsMaster bool
	prntx          bool
}

// revokeDelegationWithAllowance builds the askstop transaction: seq chain at
// input 0, delegation at input 1, request output (if any) at input 2. The
// produced delegation goes on hold with its balance reduced by `take`, and
// the sequencer chain absorbs the fee plus whatever was taken.
func (td *testData) revokeDelegationWithAllowance(par allowanceRevokeParams) error {
	txb := exhelp.New()

	_, _, err := txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	predIdx, err := txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	require.NoError(td, err)

	requestIdx := byte(0xff)
	absorbed := uint64(0)
	if par.request != nil {
		requestIdx, err = txb.ConsumeOutput(par.request.Output, par.request.ID)
		require.NoError(td, err)
		absorbed = par.request.Output.TokenBalance()
	}

	// sequencer successor absorbs the fee and the amount taken from the delegation
	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	succChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance() + absorbed + par.take))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	revokedOut, err := td.delegatedOutput.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		TxTs:                     par.ts,
		PredOutputIndex:          predIdx,
		TakeFromBalance:          par.take,
		DisableConsistencyChecks: true,
	})
	require.NoError(td, err)
	revokedIdx, err := txb.ProduceOutput(revokedOut)
	require.NoError(td, err)

	// unlock the delegation, optionally referencing the request output
	if par.unlockAsMaster {
		additional := []byte{ledger.DelegationUnlockedByMaster}
		if par.request != nil && !par.omitUnlockRef {
			additional = append(additional, requestIdx)
		}
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), additional...)
	} else {
		additional := []byte{ledger.DelegationUnlockedByTarget}
		if par.request != nil && !par.omitUnlockRef {
			additional = append(additional, requestIdx)
		}
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), additional...)
	}
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(revokedIdx))

	if par.request != nil {
		// tag-along lock is unlocked by the target chain at input 0; the
		// ensureStopDelegation at element 4 names the produced delegation
		txb.PutUnlockParams(requestIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
		txb.PutUnlockParams(requestIdx, 4, []byte{revokedIdx})
	}

	frozenCoverageDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(td, err)
	txb.MustPutFrozenCoverage(succChainIdx, frozenCoverageDelta, par.ts)

	txb.SetSequencerData(succChainIdx, txbuildercore.SequencerOutputIndexNone)
	dummyTxId := base.NewTransactionID(par.ts.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(par.ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		if par.prntx {
			return fmt.Errorf("error: '%v'\n---- failing tx ----\n%s", err, txString)
		}
		return err
	}
	return td.u.AddTransaction(txBytes)
}

// setupFrozenDelegation brings the harness to the state every allowance test
// starts from: a delegation frozen for its maximum span, which is the only
// situation in which askstop — and therefore an allowance — is meaningful.
// Outside the frozen span the master simply consumes the output itself.
func setupFrozenDelegation(t *testing.T) *testData {
	td := &testData{T: t}
	td.init()

	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	// a non-zero inflation cut: the allowance ceiling is the unearned part of
	// the advance, so a delegation whose target advanced nothing has nothing to
	// unwind and a ceiling of 0.
	_, _, err := td.initDelegationUTXOMake(ts, 4, 900)
	require.NoError(t, err)

	// the target transits the delegation into the frozen state, prepaying the advance
	ts = td.timestampSlotsForward(100)
	err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts:                       ts,
		freezeUntilEpoch:         td.delegatedOutput.FreezeUntilMax(ts),
		inflate:                  true,
		disableConsistencyChecks: true,
	})
	require.NoError(t, err)
	require.True(t, td.delegatedOutput.IsMarkedFrozen(), "delegation must be frozen for allowance tests")

	return td
}

// allowanceCase runs one edge case: build a request output granting
// `allowance` (optionally by a forger, optionally naming another
// delegation), then try to stop the delegation taking `take` out of it.
type allowanceCase struct {
	allowance      uint64
	take           uint64
	forgedSender   bool
	namesOther     bool
	omitUnlockRef  bool
	unlockAsMaster bool
}

func (td *testData) runAllowanceCase(c allowanceCase) error {
	signer, sender := td.masterPrivateKey, td.masterAddr
	if c.forgedSender {
		// a funded account that is NOT the delegation master
		keys, _, addrs := td.u.GenerateAddresses(10, 1)
		require.NoError(td, td.u.TokensFromFaucet(addrs[0], 100_000_000))
		signer, sender = keys[0], addrs[0]
	}
	named := td.delegatedOutput.ChainID
	if c.namesOther {
		named = base.RandomChainID()
	}

	req := td.makeAllowanceRequestOutput(td.timestampSlotsForward(1), signer, sender, named, c.allowance)

	return td.revokeDelegationWithAllowance(allowanceRevokeParams{
		ts:             td.timestampSlotsForward(2),
		request:        req,
		take:           c.take,
		omitUnlockRef:  c.omitUnlockRef,
		unlockAsMaster: c.unlockAsMaster,
	})
}

// TestAllowanceDecreaseWithin: the sequencer takes what the master
// authorised out of the delegation balance. This is the whole point of the
// feature — the delegator needs no liquid tokens of their own to stop.
func TestAllowanceDecreaseWithin(t *testing.T) {
	td := setupFrozenDelegation(t)

	ceiling := td.delegatedOutput.AllowanceCeiling()
	require.Greater(t, ceiling, uint64(0), "a frozen delegation must have a non-zero ceiling")
	allowance := ceiling / 2
	before := td.delegatedOutput.Output.TokenBalance()
	chainID := td.delegatedOutput.ChainID

	require.NoError(t, td.runAllowanceCase(allowanceCase{allowance: allowance, take: allowance}))

	after, err := td.u.SugaredStateReader().GetDelegatedOutput(chainID)
	require.NoError(t, err)
	require.EqualValues(t, before-allowance, after.Output.TokenBalance(),
		"the allowance must have come out of the delegation balance")
	require.True(t, after.IsMarkedOnHold(), "askstop still leaves the delegation on hold")
}

// TestAllowancePartialTake: the sequencer may take less than authorised.
func TestAllowancePartialTake(t *testing.T) {
	td := setupFrozenDelegation(t)

	allowance := td.delegatedOutput.AllowanceCeiling() / 2
	before := td.delegatedOutput.Output.TokenBalance()
	chainID := td.delegatedOutput.ChainID

	require.NoError(t, td.runAllowanceCase(allowanceCase{allowance: allowance, take: allowance / 3}))

	after, err := td.u.SugaredStateReader().GetDelegatedOutput(chainID)
	require.NoError(t, err)
	require.EqualValues(t, before-allowance/3, after.Output.TokenBalance())
}

// TestAllowanceEdgeCasesRejected: every way of overreaching must fail.
func TestAllowanceEdgeCasesRejected(t *testing.T) {
	for name, c := range map[string]func(ceiling uint64) allowanceCase{
		// one mote more than authorised
		"take_exceeds_allowance": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling/2 + 1}
		},
		// allowance above what the sequencer actually loses: an over-generous
		// wallet must not be usable to drain a delegation
		"allowance_above_ceiling": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling + 1, take: ceiling + 1}
		},
		// the allowance is only the master's to give
		"forged_sender": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, forgedSender: true}
		},
		// an allowance for delegation X must not authorise a decrease on Y
		"names_other_delegation": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, namesOther: true}
		},
		// without the third unlock byte the ordinary non-decrease rule applies,
		// even though the allowance sits right there in a consumed input
		"no_unlock_reference": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, omitUnlockRef: true}
		},
		// the master grants allowances, it does not consume them
		"master_path_with_reference": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, unlockAsMaster: true}
		},
		// no allowance authorised at all: the gate must be exactly as before
		"zero_allowance_forbids_decrease": func(uint64) allowanceCase {
			return allowanceCase{allowance: 0, take: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			td := setupFrozenDelegation(t)
			err := td.runAllowanceCase(c(td.delegatedOutput.AllowanceCeiling()))
			require.Error(t, err)
			t.Logf("rejected as expected: %v", err)
		})
	}
}

// Stopping a frozen delegation early returns an advance, it does not pay a
// penalty. The target prepaid the delegator for the whole frozen span at some
// promille share; what comes back is the part of that advance the remaining
// span will no longer earn, at the same share. So the allowance ceiling must
// scale with the share pinned at freeze time, and must stay strictly below the
// uncut projection: the target absorbs its own foregone cut rather than being
// made whole for it. Charging the uncut projection would price that cut as a
// termination penalty, payable on every add-to-delegation cycle.
func TestAllowanceCeilingIsUnwindNotPenalty(t *testing.T) {
	td := setupFrozenDelegation(t)
	d := td.delegatedOutput

	// setupFrozenDelegation freezes at the delegator's required cut
	require.EqualValues(t, 900, d.AdvanceShare, "freeze pins the share it advanced at")
	require.EqualValues(t, d.RequiredInflationCut, d.AdvanceShare)

	lib := ledger.L(d.ID.Slot())
	lastSlot := lib.LastSlotInEpochDirect(d.Target, d.LastFrozenEpoch, d.EpochSlots())
	require.Greater(t, lastSlot, d.ID.Slot(), "delegation must still be inside its frozen span")

	// the uncut projection over the same span, measured from the output's own
	// slot - the quantity AllowanceCeiling used to return in full
	uncut := lib.ChainInflationMultiStep(d.Output.TokenBalance(), d.ID.Slot(), lastSlot-d.ID.Slot()+1)
	require.Greater(t, uncut, uint64(0))

	ceiling := d.AllowanceCeiling()
	require.EqualValues(t, uncut*uint64(d.AdvanceShare)/1000, ceiling, "ceiling is the advanced share of the projection")
	require.Less(t, ceiling, uncut, "the target's own foregone cut is not charged to the delegator")
}

// ==========================================================================
// 5. What the target may change
// ==========================================================================

// delegationOriginWithTimelock creates a delegation origin carrying one
// extra constraint — a timelock — between the chain constraint and the
// delegateLockState, so the pinned range is non-empty:
// amounts(0), index values(1), delegateLock(2), chain(3), timelock(4),
// delegateLockState(5). The timelock is set to the origin slot, so it is
// long expired by the time the target transits a slot later.
func (td *testData) delegationOriginWithTimelock(t *testing.T, ts base.LedgerTime) {
	t.Helper()
	par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
	require.NoError(t, err)

	txBytes, err := utxodb.MakeSimpleTransferTransaction(
		par.WithAmount(delegatedTokens).
			WithTargetLock(ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 0)).
			WithConstraint(ledger.NewChainOrigin(ts.Slot)).
			WithConstraint(ledger.NewTimelock(ts.Slot)).
			WithConstraint(ledger.DelegateLockState{State: ledger.DelegateLockStateUndef}),
	)
	require.NoError(t, err)
	require.NoError(t, td.u.AddTransaction(txBytes))

	outs, err := td.u.SugaredStateReader().GetOutputsDelegatedToAccount2(td.target[:])
	require.NoError(t, err)
	require.EqualValues(t, 1, len(outs))
	var ok bool
	td.delegatedOutput, ok = ledger.DelegationOutputFromOutputWithChainID(outs[0])
	require.True(t, ok)
	require.EqualValues(t, 6, td.delegatedOutput.Output.NumElements(),
		"layout: amounts, index values, delegateLock, chain, timelock, state")
}

// targetTransitDelegation builds the sequencer-side transit of
// td.delegatedOutput: the target's chain at input 0 (unlocking the
// delegation), the delegation at input 1, and a successor made of the
// canonical amounts / chain / state with every other element carried over
// from the consumed output. `mutate` (may be nil) is the target's attempt to
// change something it does not own; it receives the successor builder and
// the index the delegateLockState occupies.
// Returns the validation/submission error.
func (td *testData) targetTransitDelegation(t *testing.T, mutate func(o *ledger.OutputBuilder, stateIndex byte)) error {
	t.Helper()
	delOut := td.delegatedOutput

	ts := td.timestampSlotsForward(1)
	requiredAdvance, err := delOut.RequiredMinimumInflationAdvance(ts, 0)
	require.NoError(t, err)
	canon, err := delOut.MakeDelegationFreezeOutput(ts, 0, 1, delOut.RequiredInflationCut, true)
	require.NoError(t, err)
	// In the canonical (extra-less) successor the state sits right after the
	// chain constraint; on our output it sits at the end.
	const canonStateIndex = byte(4)
	stateIndex := byte(delOut.Output.NumElements() - 1)

	txb := exhelp.New()
	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(t, err)
	seqCC := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot,
		0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	seqChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance() - requiredAdvance))
		o.PutConstraint(seqCC.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(delOut.Output, delOut.ID)
	require.NoError(t, err)
	// Target unlock: the second byte is not the master marker 0xff.
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	mustAt := func(o *ledger.Output, i byte) []byte {
		b, err := o.ConstraintAt(i)
		require.NoError(t, err)
		return b
	}
	succ := delOut.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(mustAt(canon, ledger.ConstraintIndexAmounts), ledger.ConstraintIndexAmounts)
		o.PutConstraint(mustAt(canon, ledger.ConstraintIndexChain), ledger.ConstraintIndexChain)
		o.PutConstraint(mustAt(canon, canonStateIndex), stateIndex)
		if mutate != nil {
			mutate(o, stateIndex)
		}
	})
	_, err = txb.ProduceOutput(succ)
	require.NoError(t, err)

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(t, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, ts)

	txb.SetSequencerData(seqChainIdx, txbuildercore.SequencerOutputIndexNone)
	txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))
	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, failedTx, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		t.Logf("target transit rejected:\n%s", failedTx)
		return err
	}
	return td.u.AddTransaction(txBytes)
}

// Calibration: carrying the extras over untouched is the ordinary transit
// and must stay valid — the pinned range is compared, not forbidden.
func TestDelegationTargetTransitPreservingExtrasAccepted(t *testing.T) {
	td := &testData{T: t}
	td.init()
	td.delegationOriginWithTimelock(t, td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace)))

	require.NoError(t, td.targetTransitDelegation(t, nil),
		"a target transit that leaves the extras alone must validate")
}

// Modifying an extra: the target rewrites the timelock the master attached.
func TestDelegationTargetCannotModifyExtras(t *testing.T) {
	td := &testData{T: t}
	td.init()
	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	td.delegationOriginWithTimelock(t, ts)

	err := td.targetTransitDelegation(t, func(o *ledger.OutputBuilder, _ byte) {
		// A far-future timelock would freeze the delegation for the master.
		o.PutConstraint(ledger.NewTimelock(ts.Slot+100_000).Bytes(), 4)
	})
	require.Error(t, err, "the target must not be able to rewrite a constraint it does not own")
	require.NoError(t, util.MustErrorWith(err, "delegation target cannot modify constraints"))
}

// Adding one: inserted before the state so the "state must be last" rule is
// satisfied and the count check is what refuses it.
func TestDelegationTargetCannotAddConstraints(t *testing.T) {
	td := &testData{T: t}
	td.init()
	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	td.delegationOriginWithTimelock(t, ts)

	err := td.targetTransitDelegation(t, func(o *ledger.OutputBuilder, stateIndex byte) {
		stateBytes, err := o.Tuple().At(int(stateIndex))
		require.NoError(t, err)
		o.PutConstraint(ledger.NewTimelock(ts.Slot+100_000).Bytes(), stateIndex)
		o.PutConstraint(stateBytes, stateIndex+1)
	})
	require.Error(t, err, "the target must not be able to attach anything to the chain")
	require.NoError(t, util.MustErrorWith(err, "delegation target cannot add or remove constraints"))
}

// And dropping one: the timelock disappears, the state moves up.
func TestDelegationTargetCannotDropConstraints(t *testing.T) {
	td := &testData{T: t}
	td.init()
	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	td.delegationOriginWithTimelock(t, ts)

	err := td.targetTransitDelegation(t, func(o *ledger.OutputBuilder, stateIndex byte) {
		stateBytes, err := o.Tuple().At(int(stateIndex))
		require.NoError(t, err)
		o.PutConstraint(stateBytes, 4) // state takes the timelock's place
		o.PutConstraint(nil, stateIndex)
	})
	require.Error(t, err, "the target must not be able to drop a constraint")
}
