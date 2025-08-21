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
	seqOnChainBalance             = 199_999_000_000
	delegatedTokens               = 1_000_000_000
)

type testData struct {
	*testing.T
	u          *utxodb.UTXODB
	target     ledger.ChainLock
	masterAddr ledger.AddressED25519

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
	par, err := td.u.MakeTransferInputData(td.seqPrivateKey, nil, ledger.TimeNow().AddSlots(1))
	require.NoError(td, err)
	outs, err := td.u.DoTransferOutputs(par.
		WithAmount(seqOnChainBalance).
		WithTargetLock(seqControllerAddr).
		WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot, seqOnChainBalance)),
	)
	require.NoError(td, err)
	require.EqualValues(td, 2, len(outs))
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(td, err)
	require.EqualValues(td, 1, len(chOuts))

	td.seqChainOrigin = *chOuts[0]
	td.Logf("seq chain origin:\n%s", td.seqChainOrigin.String())

	td.target = ledger.ChainLockFromChainID(td.seqChainOrigin.ChainID)
	td.Logf("==== master address    : %s (%s)", td.masterAddr.String(), util.Th(td.u.Balance(td.masterAddr)))
	td.Logf("==== seq controller    : %s (%s)", seqControllerAddr.String(), util.Th(td.u.Balance(seqControllerAddr)))
	_, onChain, err := td.u.BalanceOnChain(td.seqChainOrigin.ChainID)
	require.NoError(td, err)
	td.Logf("==== seq on-chain      : %s", util.Th(onChain))
	td.Logf("==== delegation target : %s (%s)", td.target.String(), util.Th(td.u.Balance(td.target)))
}

func (td *testData) initDelegationUTXODirect(ts base.LedgerTime, revoked bool, maxFreezeSlots uint16, prnOnError bool) ([]byte, error) {
	var txBytes []byte

	// create delegation output
	par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
	if err != nil {
		return nil, err
	}

	delegationLock := ledger.NewDelegateLock(td.target, td.masterAddr, maxFreezeSlots, 0)
	txBytes, err = txbuilder.MakeSimpleTransferTransaction(par.
		WithAmount(delegatedTokens).
		WithTargetLock(delegationLock).
		WithConstraint(ledger.NewChainOrigin(ts.Slot, delegatedTokens)).
		WithConstraint(ledger.DelegateLockState{IsRevoked: revoked}),
	)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err = td.u.AddTransaction(txBytes); err == nil {
		outs, err := td.u.SugaredStateReader().GetOutputsDelegatedToAccount2(td.target)
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
	require.EqualValues(t, 30, ledger.DelegationConst().SafeRevocationSlots)

	td := &testData{T: t}

	var err error

	t.Run("ok 1", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, err = td.initDelegationUTXODirect(ts, false, 1, true)
		require.NoError(t, err)
	})
	t.Run("ok 2", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, err = td.initDelegationUTXODirect(ts, false, 1337, true)
		require.NoError(t, err)
	})
	t.Run("fail", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, err = td.initDelegationUTXODirect(ts, true, 1, true)
		util.RequireErrorWith(t, err, "wrong delegation origin parameters")
	})
}

const tagAlongFee = 500

func (td *testData) initDelegationUTXOMake(ts base.LedgerTime, maxFreezeSlots uint16, minInflationAdvancePerEpoch uint64) ([]byte, string, error) {
	outs, availableTokens := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, delegatedTokens+tagAlongFee)
	require.True(td, availableTokens >= delegatedTokens+tagAlongFee)

	txBytes, err := txbuilder.MakeDelegationInitTransaction(txbuilder.MakeDelegationInitTransactionParams{
		Timestamp:                   ts,
		Amount:                      delegatedTokens,
		Master:                      td.masterAddr,
		Target:                      td.target,
		MaxFreezeSlots:              maxFreezeSlots,
		MinInflationAdvancePerEpoch: minInflationAdvancePerEpoch,
		MasterPrivateKey:            td.masterPrivateKey,
		Inputs:                      outs,
		TagAlongSequencer:           base.RandomChainID(),
		TagAlongFee:                 tagAlongFee,
	})
	txString := td.u.TxToSource(txBytes)
	if err != nil {
		return nil, txString, err
	}
	tx, err := transaction.FromBytes(txBytes)
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
	from, to := td.delegatedOutput.SafeRevocationSlots()
	td.Logf(">>>> transit %d, -> %s, safe revocation from %d to %d, unfreeze slot: %d",
		n, par.ts.String(), from, to, td.delegatedOutput.UnfreezeSlot())

	txb := txbuilder.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, 2, td.seqChainOrigin.OriginSlot, td.seqChainOrigin.OriginAmount)
	seqChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), 2)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

	delegatedOutPar := ledger.MakeDelegationSuccessorOutputParams{
		TxTs:                     par.ts,
		FreezeUntilEpoch:         par.freezeUntilEpoch,
		DisableConsistencyChecks: par.disableConsistencyChecks,
	}
	delegatedOutPar.PredOutputIndex, err = txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	if par.inflate {
		delegatedOutPar.Inflation = ledger.L().CalcChainInflationAmountOneSlot(td.delegatedOutput.Timestamp().Slot, td.delegatedOutput.Output.TokenBalance())
	}
	delegatedOut, err := td.delegatedOutput.MakeDelegationFreezeOutput(delegatedOutPar)
	require.NoError(td, err)

	txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))
	txb.PutUnlockParams(1, 2, ledger.NewChainUnlockParams(1, 2))

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
		td.Logf("%s", td.delegatedOutput.LinesSource("     ").String())
	}

	// get chain tip
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)

	return
}

func (td *testData) revokeDelegation(ts base.LedgerTime, inflate, prntx bool) (err error) {
	require.NoError(td, err)

	diffSlots := ts.Slot - td.delegatedOutput.Timestamp().Slot
	diffEpochs := ledger.DelegationConst().DiffEpochs(td.delegatedOutput.Target.ChainID(), ts, td.delegatedOutput.Timestamp())
	td.Logf(">>>> revoke -----\nts = %s, diffSlots = %d, diffEpochs = %d\n-----\n%s",
		ts.String(), diffSlots, diffEpochs, td.delegatedOutput.LinesSource("   ").String())

	txb := txbuilder.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, 2, td.seqChainOrigin.OriginSlot, td.seqChainOrigin.OriginAmount)
	succChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), 2)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

	inflation := uint64(0)
	if inflate {
		inflation = ledger.L().CalcChainInflationAmountOneSlot(td.delegatedOutput.Timestamp().Slot, td.delegatedOutput.Output.TokenBalance())
	}
	delegatedOutPar := ledger.MakeDelegationRevokeOutputParams{
		Timestamp:                ts,
		Inflation:                inflation,
		DisableConsistencyChecks: true,
	}
	delegatedOutPar.PredOutputIndex, err = txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	delegatedOut, err := td.delegatedOutput.MakeDelegationRevokeOutput(delegatedOutPar)
	require.NoError(td, err)

	txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))
	txb.PutUnlockParams(1, 2, ledger.NewChainUnlockParams(1, 2))

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
		td.Logf("%s", td.delegatedOutput.LinesSource("     ").String())
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

func (td *testData) timestampSlotsForward(slots base.Slot) base.LedgerTime {
	ts := base.MaximumTime(td.seqChainOrigin.Timestamp(), td.delegatedOutput.Timestamp())
	return ts.AddSlots(slots)
}

func (td *testData) discontinueDelegation(ts base.LedgerTime, prntx bool) error {

	txb := txbuilder.New()
	amount, _, err := txb.ConsumeOutputsNoUnlock(&td.delegatedOutput.OutputWithID)
	require.NoError(td, err)

	txb.PutUnlockParams(0, 2, ledger.FinishChainUnlockParams)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount - tagAlongFee)).WithLock(td.masterAddr)
	}))
	require.NoError(td, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(tagAlongFee).WithLock(ledger.ChainLockFromChainID(base.RandomChainID()))
	}))
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

func TestDelegationLock2Consume(t *testing.T) {
	td := &testData{T: t}

	var err error
	var txString string
	_ = txString

	t.Run("init", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, txString, err = td.initDelegationUTXOMake(ts, 4, 0)
		require.NoError(t, err)
		//td.Logf("---------------- transaction -----------------\n%s", txString)
	})
	t.Run("master+init+kill", func(t *testing.T) {
		// create delegation output and destroy it with next tx
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, 0)
		require.NoError(t, err)
		unfreezeSlot := td.delegatedOutput.UnfreezeSlot()
		//ts = base.NewLedgerTime(base.Slot(unfreezeSlot-1), 10)

		ts = td.delegatedOutput.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		t.Logf("ts: %s, unfreeze: %d", ts.String(), unfreezeSlot)

		err = td.discontinueDelegation(ts, true)
		require.NoError(t, err)
	})
	t.Run("target+init", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:               td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
			freezeUntilEpoch: 0,
			prntx:            false,
		})
		require.NoError(t, err)
	})
	t.Run("target_test_safe_revocation_window", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 512, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:               td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
			freezeUntilEpoch: 0,
			prntx:            false,
		})
		require.NoError(t, err)

		from, to := td.delegatedOutput.SafeRevocationSlots()
		td.Logf("safe revocation from=%d, to=%d", from, to)

		err = td.transitChainWithDelegationWithMake(2, transitWithMakeParams{
			ts: base.MaximumTime(
				td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
				base.NewLedgerTime(base.Slot(from), 5),
			),
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		util.RequireErrorWith(t, err, "delegation target should not be unlocked inside safe revocation window")

		err = td.transitChainWithDelegationWithMake(3, transitWithMakeParams{
			ts: base.MaximumTime(
				td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
				base.NewLedgerTime(base.Slot(to-1), 5),
			),
			disableConsistencyChecks: true,
			prntx:                    false,
		})
		util.RequireErrorWith(t, err, "delegation target should not be unlocked inside safe revocation window")

		err = td.transitChainWithDelegationWithMake(4, transitWithMakeParams{
			ts: base.MaximumTime(
				td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
				base.NewLedgerTime(base.Slot(to+1), 5),
			),
			disableConsistencyChecks: true,
			prntx:                    false,
		})
		require.NoError(t, err)
	})
	t.Run("target_freeze_ok", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, 0)
		require.NoError(t, err)

		//t.Logf("=========\n%s", td.delegatedOutput.OutputWithID.String())

		ts = td.timestampSlotsForward(1000)
		txEpoch := ledger.DelegationConst().EpochFromSlot(td.delegatedOutput.Target.ChainID(), ts.Uint32())
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)
		frozenEpochs := freezeUntilEpoch - txEpoch + 1
		frozenSlots := ledger.DelegationConst().FrozenSlotsFromFrozenEpochs(td.delegatedOutput.Target.ChainID(), uint32(ts.Slot), byte(frozenEpochs))
		t.Logf(">>>>>>>>> freezeUntilEpoch: %d, frozenEpochs: %d, frozenSlots: %d", freezeUntilEpoch, frozenEpochs, frozenSlots)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)
	})
	t.Run("target_freeze_wrong_last_frozen_epoch", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, 0)
		require.NoError(t, err)

		//t.Logf("=========\n%s", td.delegatedOutput.OutputWithID.String())

		ts = td.timestampSlotsForward(500)
		txEpoch := ledger.DelegationConst().EpochFromSlot(td.delegatedOutput.Target.ChainID(), ts.Uint32())
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)

		frozenEpochs := freezeUntilEpoch - txEpoch + 1
		frozenSlots := ledger.DelegationConst().FrozenSlotsFromFrozenEpochs(td.delegatedOutput.Target.ChainID(), uint32(ts.Slot), byte(frozenEpochs))
		t.Logf(">>>>>>>>> freezeUntilEpoch: %d, frozenEpochs: %d, frozenSlots: %d", freezeUntilEpoch, frozenEpochs, frozenSlots)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch + 1,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		util.RequireErrorWith(t, err, "frozen slots cannot exceed maximum set by delegator")
	})
	t.Run("target_freeze_ok_inflate", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 2048, 0)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(100)
		freezeUntilEpoch := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)
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
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 3000, 0)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		txEpoch := ledger.DelegationConst().EpochFromSlot(td.delegatedOutput.Target.ChainID(), ts.Uint32())
		_ = txEpoch
		freezeUntilEpoch := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)
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
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 3000, 0)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		freezeUntilEpoch := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)

		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)

		unfreeze := td.delegatedOutput.UnfreezeSlot()

		ts = base.NewLedgerTime(base.Slot(unfreeze)-100, 5)
		err = td.discontinueDelegation(ts, false)
		util.RequireErrorWith(t, err, "master can't unlock frozen delegation output")

		ts = base.NewLedgerTime(base.Slot(unfreeze-1), 5)
		err = td.discontinueDelegation(ts, false)
		util.RequireErrorWith(t, err, "master can't unlock frozen delegation output")

		ts = base.NewLedgerTime(base.Slot(unfreeze), 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, err)
	})
	t.Run("revoke", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 3000, 0)
		require.NoError(t, err)

		// freeze for 512 slots
		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		freezeUntilEpoch := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)
		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntilEpoch,
			prntx:                    false,
			disableConsistencyChecks: false,
		})
		require.NoError(t, err)

		unfreeze := td.delegatedOutput.UnfreezeSlot()
		// dconst.UnfreezeSlotFromFrozenEpochs(td.target.ChainID(), uint32(ts.Slot), byte(frozenEpochs))

		// fail to unlock by master
		ts = base.NewLedgerTime(base.Slot(unfreeze)-10, 5)
		err = td.discontinueDelegation(ts, false)
		util.RequireErrorWith(t, err, "master can't unlock frozen delegation output")

		// succeed to unlock by target to mark output revoked
		err = td.revokeDelegation(ts, false, true)
		require.NoError(t, err)

		// fail to unlock by target revoked delegation
		ts = td.timestampSlotsForward(20)
		err = td.revokeDelegation(ts, false, false)
		util.RequireErrorWith(t, err, "revoked delegation cannot be unlocked by the target")

		// succeed to kill the delegation chain by master
		ts = td.timestampSlotsForward(1000)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, err)

	})
	t.Run("revoke-inflate", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 3000, 0)
		require.NoError(t, err)

		// freeze for 512 slots
		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		freezeUntil := td.delegatedOutput.LatestPossibleEpochToFreeze(ts)
		err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
			ts:                       ts,
			freezeUntilEpoch:         freezeUntil,
			prntx:                    false,
			disableConsistencyChecks: true,
		})
		require.NoError(t, err)

		unfreeze := td.delegatedOutput.UnfreezeSlot()

		// fail to unlock by master
		ts = base.NewLedgerTime(base.Slot(unfreeze)-10, 5)
		err = td.discontinueDelegation(ts, false)
		util.RequireErrorWith(t, err, "master can't unlock frozen delegation output")

		// succeed to unlock by target to mark output revoked
		err = td.revokeDelegation(ts, true, false)
		require.NoError(t, err)

		// fail to unlock by target revoked delegation
		ts = td.timestampSlotsForward(20)
		err = td.revokeDelegation(ts, true, false)
		util.RequireErrorWith(t, err, "revoked delegation cannot be unlocked by the target")

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

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, 2, td.seqChainOrigin.OriginSlot, td.seqChainOrigin.OriginAmount)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.seqChainOrigin.Output.Lock())
		o.MustPushConstraint(successorChainConstraint.Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(2).Bytes())
	}))
	util.AssertNoError(err)

	amounts = append([]int64{int64(td.delegatedOutput.Output.TokenBalance() + par.inflationAdvance), 0}, par.successorFrozenCoverage...)

	cc := ledger.NewChainConstraint(td.delegatedOutput.ChainID, 1, 2, td.delegatedOutput.OriginSlot, td.delegatedOutput.OriginAmount)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.delegatedOutput.Output.Lock())
		o.MustPushConstraint(cc.Bytes())
		freezeUntil := uint32(0)
		if par.frozenEpochs > 0 {
			txEpoch := ledger.DelegationConst().EpochFromSlot(td.delegatedOutput.Target.ChainID(), par.ts.Slot.Uint32())
			freezeUntil = txEpoch + uint32(par.frozenEpochs) - 1
		}
		o.MustPushConstraint(ledger.DelegateLockState{
			LastFrozenEpoch: freezeUntil,
			IsRevoked:       false,
		}.Bytes())
	}))
	util.AssertNoError(err)

	// unlock
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

	txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))
	txb.PutUnlockParams(1, 2, ledger.NewChainUnlockParams(1, 2))

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
		td.Logf("%s", td.delegatedOutput.LinesSource("     ").String())
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
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
			frozenEpochs:            0,
			successorFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			sequencerFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			prntx:                   true,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 1", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 1
			minInflationAdvancePerEpoch = 10
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			inflationAdvance:        advance,
			prntx:                   true,
		})
		require.NoError(t, err)
	})
	t.Run("frozen epochs 1 fail 1", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 1
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(1000)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
		advance -= 1
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance() + advance)},
			inflationAdvance:        advance,
			prntx:                   true,
		})
		util.RequireErrorWith(t, err, "not enough inflation advance")
	})
	t.Run("frozen epochs 1 fail 2", func(t *testing.T) {
		// TODO -- sometimes fails
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 1
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(700)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		require.True(t, advance > 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance())},
			sequencerFrozenCoverage: []int64{int64(td.delegatedOutput.Output.TokenBalance())},
			inflationAdvance:        advance,
			prntx:                   true,
		})
		util.RequireErrorWith(t, err, "wrong frozen coverage value")
	})
	t.Run("frozen epochs 2", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 2
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
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
			frozenEpochs                = 2
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0) - 1
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
		fc := int64(td.delegatedOutput.Output.TokenBalance() + advance)
		err = td.transitChainWithDelegationRaw(transitRawParams{
			ts:                      ts,
			frozenEpochs:            frozenEpochs,
			successorFrozenCoverage: []int64{fc, fc},
			sequencerFrozenCoverage: []int64{fc, fc},
			inflationAdvance:        advance,
			prntx:                   true,
		})
		util.RequireErrorWith(t, err, "not enough inflation advance")
	})
	t.Run("frozen epochs 3", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 3
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 3000, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
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
			frozenEpochs                = 3
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 3000, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0) - 1
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
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
		util.RequireErrorWith(t, err, "not enough inflation advance")
	})
	t.Run("frozen epochs 4", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 4
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 5000, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
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
			frozenEpochs                = 4
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 5000, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0) - 1
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
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
		util.RequireErrorWith(t, err, "not enough inflation advance")
	})
	t.Run("frozen epochs 5 fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		const (
			frozenEpochs                = 5
			minInflationAdvancePerEpoch = 100
		)
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 10000, minInflationAdvancePerEpoch)
		require.NoError(t, err)

		ts = td.timestampSlotsForward(900)
		advance := td.delegatedOutput.ProjectedInflation(ts, frozenEpochs, 0)
		t.Logf("ts: %s, frozen epcohs: %d, min advance per epoch: %d, required advance: %v", ts.String(), frozenEpochs, minInflationAdvancePerEpoch, advance)
		covVect := []int64{
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
			prntx:                   true,
		})
		util.RequireErrorWith(t, err, "frozen epochs cannot exceed")
	})
}

type transitParams struct {
	ts                      base.LedgerTime
	frozenEpochs            byte
	successorFrozenCoverage []int64
	sequencerFrozenCoverage []int64
	inflationAdvance        uint64
	prntx                   bool
}

func (td *testData) transitChainWithDelegation(par transitParams) (err error) {
	txb := txbuilder.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID, &td.delegatedOutput.OutputWithID)
	util.AssertNoError(err)

	amounts := append([]int64{int64(td.seqChainOrigin.Output.TokenBalance() - par.inflationAdvance), 0}, par.sequencerFrozenCoverage...)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, 2, td.seqChainOrigin.OriginSlot, td.seqChainOrigin.OriginAmount)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.seqChainOrigin.Output.Lock())
		o.MustPushConstraint(successorChainConstraint.Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(2).Bytes())
	}))
	util.AssertNoError(err)

	amounts = append([]int64{int64(td.delegatedOutput.Output.TokenBalance() + par.inflationAdvance), 0}, par.successorFrozenCoverage...)

	cc := ledger.NewChainConstraint(td.delegatedOutput.ChainID, 1, 2, td.delegatedOutput.OriginSlot, td.delegatedOutput.OriginAmount)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(amounts...)
		o.WithLock(td.delegatedOutput.Output.Lock())
		o.MustPushConstraint(cc.Bytes())
		freezeUntil := uint32(0)
		if par.frozenEpochs > 0 {
			txEpoch := ledger.DelegationConst().EpochFromSlot(td.delegatedOutput.Target.ChainID(), par.ts.Slot.Uint32())
			freezeUntil = txEpoch + uint32(par.frozenEpochs) - 1
		}
		o.MustPushConstraint(ledger.DelegateLockState{
			LastFrozenEpoch: freezeUntil,
			IsRevoked:       false,
		}.Bytes())
	}))
	util.AssertNoError(err)

	// unlock
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

	txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))
	txb.PutUnlockParams(1, 2, ledger.NewChainUnlockParams(1, 2))

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
		td.Logf("%s", td.delegatedOutput.LinesSource("     ").String())
	}

	// get chain tip
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)

	return
}

func TestFrozenCoverage2(t *testing.T) {
	td := &testData{T: t}
	var err error
	var txString string
	_ = txString

	t.Run("init", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024, 0)
		require.NoError(t, err)

		err = td.transitChainWithDelegation(transitParams{
			ts:                      td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
			frozenEpochs:            0,
			successorFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			sequencerFrozenCoverage: []int64{td.delegatedOutput.Output.Amounts().Amount(0)},
			prntx:                   true,
		})
		require.NoError(t, err)
	})
}
