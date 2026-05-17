package tests

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ed25519"
)

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

	// create chain for sequencer
	// Use NilLedgerTime first to populate inputs, then derive timestamp from actual input
	par, err := td.u.MakeTransferInputData(td.seqPrivateKey, nil, base.NilLedgerTime)
	require.NoError(td, err)
	par.Timestamp = par.Inputs[0].ID.Timestamp().AddSlots(1)
	outs, err := td.u.DoTransferOutputs(par.
		WithAmount(seqOnChainBalance).
		WithTargetLock(seqControllerAddr).
		WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)).
		// Per Phase 3 of delegation_epoch_params, the chain must attach
		// delegationParams at the dedicated index in order to accept
		// delegations (carry frozen-coverage receipts). Use the library
		// defaults for tests.
		WithConstraint(ledger.NewDelegationParams(
			ledger.L(0).DelegationEpochSlots,
			byte(ledger.L(0).MaxFrozenEpochs),
		), ledger.ConstraintIndexDelegationParams),
	)
	require.NoError(td, err)
	require.EqualValues(td, 2, len(outs))
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(td, err)
	require.EqualValues(td, 1, len(chOuts))

	td.seqChainOrigin = *chOuts[0]
	td.Logf("seq chain origin:\n%s", td.seqChainOrigin.String())

	td.target = td.seqChainOrigin.ChainID
	td.Logf("==== master address    : %s (%s)", td.masterAddr.String(), util.Th(td.u.Balance(td.masterAddr)))
	td.Logf("==== seq controller    : %s (%s)", seqControllerAddr.String(), util.Th(td.u.Balance(seqControllerAddr)))
	_, onChain, err := td.u.BalanceOnChain(td.seqChainOrigin.ChainID)
	require.NoError(td, err)
	td.Logf("==== seq on-chain      : %s", util.Th(onChain))
	td.Logf("==== delegation target : %s (%s)", td.target.String(), util.Th(td.u.Balance(ledger.ChainLockFromChainID(td.target))))
}

func (td *testData) delegationOriginDirect(ts base.LedgerTime, revoked bool, maxFrozenEpochs byte, inflationShare uint16, prnOnError bool) ([]byte, error) {
	var txBytes []byte

	// create delegation output
	par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
	if err != nil {
		return nil, err
	}

	// Phase 2 of delegation_epoch_params: inline the target chain's
	// (epochSlots, maxFrozenEpochs) into the lock body. Tests pre-dating
	// the per-target params refactor use the library defaults, matching
	// the old global-constant behaviour exactly.
	lib := ledger.L(ts.Slot)
	delegationLock := ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), maxFrozenEpochs, inflationShare,
		lib.DelegationEpochSlots, byte(lib.MaxFrozenEpochs))
	s := ledger.DelegateLockStateUndef
	if revoked {
		s = ledger.DelegateLockStateOnHold
	}
	txBytes, err = txbuilder.MakeSimpleTransferTransaction(
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
		require.NoError(t, util.MustErrorWith(err, "max required inflation share must be in promille less or equal than 1000"))
	})
}

const tagAlongFee = 500

func (td *testData) initDelegationUTXOMake(ts base.LedgerTime, maxFrozenEpochs byte, inflationShare uint16) ([]byte, string, error) {
	outs, availableTokens := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, delegatedTokens+tagAlongFee)
	require.True(td, availableTokens >= delegatedTokens+tagAlongFee)

	txBytes, err := txbuilder.MakeDelegationInitTransaction(txbuilder.MakeDelegationInitTransactionParams{
		Timestamp:              ts,
		Amount:                 delegatedTokens,
		MasterID:               base.HolderID(td.masterAddr),
		Target:                 td.target,
		MaxFrozenEpochs:        maxFrozenEpochs,
		RequiredInflationShare: inflationShare,
		MasterPrivateKey:       td.masterPrivateKey,
		Inputs:                 outs,
		TagAlongSequencer:      base.RandomChainID(),
		TagAlongFee:            tagAlongFee,
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
	delegatedOut, err := td.delegatedOutput.MakeDelegationFreezeOutput(par.ts, par.freezeUntilEpoch, 1, requiredAdvance, par.disableConsistencyChecks)
	if err != nil {
		return err
	}

	txb := txbuilder.New()

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

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = par.ts
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()
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
	diffEpochs := lib.DiffEpochs(td.delegatedOutput.Target, ts, td.delegatedOutput.Timestamp(), td.delegatedOutput.EpochSlots)
	td.Logf(">>>> revoke -----\nts = %s, diffSlots = %d, diffEpochs = %d\n-----\n%s",
		ts.String(), diffSlots, diffEpochs, td.delegatedOutput.LinesSourceFull("   ").String())

	txb := txbuilder.New()

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

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()
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

	txb := txbuilder.New()

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

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(td.masterPrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()
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
		txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, ts.Slot, td.delegatedOutput.EpochSlots)
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)
		frozenEpochs := freezeUntilEpoch - txEpoch + 1
		frozenSlots := ledger.L(0).FrozenSlotsFromFrozenEpochs(td.delegatedOutput.Target, ts.Slot, td.delegatedOutput.EpochSlots, byte(frozenEpochs))
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
		txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, uint32(ts.Slot), td.delegatedOutput.EpochSlots)
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.FreezeUntilMax(ts)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch + 1,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, util.MustErrorWith(err, "frozen epochs cannot exceed maximum set by delegator"))
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
		txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, ts.Slot, td.delegatedOutput.EpochSlots)
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
	prntx                   bool
}

func (td *testData) transitChainWithDelegationRaw(par transitRawParams) (err error) {
	txb := txbuilder.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID, &td.delegatedOutput.OutputWithID)
	util.AssertNoError(err)

	amounts := append([]int64{int64(td.seqChainOrigin.Output.TokenBalance() - par.inflationAdvance), 0}, par.sequencerFrozenCoverage...)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	// Carry over the delegationParams constraint at its fixed index so
	// the chain's immutability check (selfImmutableOnSuccessorIndex(6))
	// passes across transit. Phase 3 of delegation_epoch_params.
	dpBytes, _ := td.seqChainOrigin.Output.At(int(ledger.ConstraintIndexDelegationParams))
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.seqChainOrigin.Output.Lock())
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.NewSequencerConstraint().Bytes())
		if len(dpBytes) > 0 {
			o.PutConstraint(dpBytes, ledger.ConstraintIndexDelegationParams)
		}
	}))
	util.AssertNoError(err)

	amounts = append([]int64{int64(td.delegatedOutput.Output.TokenBalance() + par.inflationAdvance), 0}, par.successorFrozenCoverage...)

	cc := ledger.NewChainConstraint(td.delegatedOutput.ChainID, 1, td.delegatedOutput.OriginSlot, 0, 0, td.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.delegatedOutput.Output.Lock())
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		freezeUntil := uint32(0)
		if par.frozenEpochs > 0 {
			txEpoch := ledger.L(0).EpochFromSlotDirect(td.delegatedOutput.Target, par.ts.Slot, td.delegatedOutput.EpochSlots)
			freezeUntil = txEpoch + uint32(par.frozenEpochs) - 1
		}
		o.MustPushConstraint(ledger.DelegateLockState{
			LastFrozenEpoch: freezeUntil,
			State:           ledger.DelegateLockStateFrozen,
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

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = par.ts
	txb.TransactionData.SequencerOutputIndex = 0
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()
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
			prntx:                   true,
		})
		require.NoError(t, util.MustErrorWith(err, "not enough inflation advance"))
	})
	t.Run("frozen epochs 1", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 1
			inflationShare = 10
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			inflationAdvance:        advance,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 1 fail 1", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 1
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1000)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
		advance -= 1
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			inflationAdvance:        advance,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "not enough inflation advance"))
	})
	t.Run("frozen epochs 1 fail 2", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 1
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		require.True(t, advance > 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance())},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance())},
			inflationAdvance:        advance,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "wrong frozen coverage value"))
	})
	t.Run("frozen epochs 2", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 2
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(ts, frozenEpochs)
		require.NoError(t, err)

		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
		fc := int64(td.delegatedOutput.Output.TokenBalance() + advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{fc, fc},
			sequencerFrozenCoverage: []int64{fc, fc},
			inflationAdvance:        advance,
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 2 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 2
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
		fc := int64(td.delegatedOutput.Output.TokenBalance() + advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{fc, fc},
			sequencerFrozenCoverage: []int64{fc, fc},
			inflationAdvance:        advance,
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "not enough inflation advance"))
	})
	t.Run("frozen epochs 3", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 3
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(ts, frozenEpochs)
		require.NoError(t, err)

		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
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
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 3 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 3
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
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
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "not enough inflation advance"))
	})
	t.Run("frozen epochs 4", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 4
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
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
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 4 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 4
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 4, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
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
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "not enough inflation advance"))
	})
	t.Run("frozen epochs 8", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 8
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, frozenEpochs, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvanceByFrozenEpochs(ts, frozenEpochs)
		require.NoError(t, err)
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
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
			prntx:                   false,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 8 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs   = 8
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, frozenEpochs, inflationShare)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance, err := td.delegatedOutput.RequiredMinimumInflationAdvance(ts, frozenEpochs)
		require.NoError(t, err)
		advance -= 1
		t.Logf("ts: %s, frozen epochs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, inflationShare, advance)
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
			prntx:                   false,
		})
		require.NoError(t, util.MustErrorWith(err, "not enough inflation advance"))
	})
	t.Run("frozen epochs 9 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			inflationShare = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, byte(ledger.L(0).MaxFrozenEpochs+1), inflationShare)
		require.NoError(t, util.MustErrorWith(err, "wrong max frozen epochs value"))
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
