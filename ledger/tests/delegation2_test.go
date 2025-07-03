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
	delegatedOutput                 ledger.Delegate2Output
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

	delegationLock := ledger.NewDelegate2Lock(td.target, td.masterAddr, maxFreezeSlots)
	txBytes, err = txbuilder.MakeSimpleTransferTransaction(par.
		WithAmount(delegatedTokens).
		WithTargetLock(delegationLock).
		WithConstraint(ledger.NewChainOrigin(ts.Slot, delegatedTokens)).
		WithConstraint(ledger.DelegateLock2State{Revoked: revoked}),
	)
	if err != nil && prnOnError {
		td.Logf(">>>>> %v\n============ transaction ==============\n%s", err, td.u.TxToSource(txBytes))
		return nil, err
	}
	if err = td.u.AddTransaction(txBytes); err == nil {
		outs, err := td.u.SugaredStateReader().GetOutputsDelegatedToAccount2(td.target)
		require.NoError(td, err)
		require.EqualValues(td, 1, len(outs))
		td.delegatedOutput, err = ledger.AsDelegate2Output(outs[0])
		require.NoError(td, err)
		td.Logf("delegation ID: %s", td.delegatedOutput.ChainID.String())
		td.Logf("delegated UTXO:\n%s", td.delegatedOutput.Output.ToSource("     "))
	}
	return txBytes, err

}

func TestDelegationLock2Init(t *testing.T) {
	require.EqualValues(t, 30, ledger.DelegationSafeRevocationSlots())

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
		util.RequireErrorWith(t, err, "wrong start parameters")
	})
}

const tagAlongFee = 500

func (td *testData) initDelegationUTXOMake(ts base.LedgerTime, maxFreezeSlots uint16) ([]byte, string, error) {
	outs, availableTokens := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(td.masterAddr, delegatedTokens+tagAlongFee)
	require.True(td, availableTokens >= delegatedTokens+tagAlongFee)

	txBytes, err := txbuilder.MakeDelegationInitTransaction(txbuilder.MakeDelegationInitTransactionParams{
		Timestamp:         ts,
		Amount:            delegatedTokens,
		Master:            td.masterAddr,
		Target:            td.target,
		MaxFreezeSlots:    maxFreezeSlots,
		MasterPrivateKey:  td.masterPrivateKey,
		Inputs:            outs,
		TagAlongSequencer: base.RandomChainID(),
		TagAlongFee:       tagAlongFee,
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
	td.delegatedOutput, err = ledger.AsDelegate2Output(dc)
	require.NoError(td, err)

	err = td.u.AddTransaction(txBytes)
	return txBytes, txString, err

}

type transitParams struct {
	ts              base.LedgerTime
	delegationState ledger.DelegateLock2State
	prntx           bool
}

func (td *testData) transitChainWithDelegation(n int, par transitParams) (err error) {
	td.Logf(">>>> transit %d", n)
	txb := txbuilder.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, 2, td.seqChainOrigin.OriginSlot, td.seqChainOrigin.OriginAmount)
	_, err = txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successorChainConstraint.Bytes(), 2)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2))

	// transit delegation
	_, _, err = txb.ConsumeOutputsNoUnlock(&td.delegatedOutput.OutputWithID)
	require.NoError(td, err)

	txb.PutUnlockParams(1, 1, ledger.NewChainLockUnlockParams(0, 2))
	txb.PutUnlockParams(1, 2, ledger.NewChainUnlockParams(1, 2))

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmount(td.delegatedOutput.Output.Amount())
		o.WithLock(td.delegatedOutput.Output.Lock())
		o.MustPushConstraint(ledger.NewChainConstraint(td.delegatedOutput.ChainID, 1, 2, td.delegatedOutput.OriginSlot, td.delegatedOutput.OriginAmount).Bytes())
		o.MustPushConstraint(par.delegationState.Bytes())
	}))
	require.NoError(td, err)

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
		o.WithAmount(amount - tagAlongFee).WithLock(td.masterAddr)
	}))
	require.NoError(td, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmount(tagAlongFee).WithLock(ledger.ChainLockFromChainID(base.RandomChainID()))
	}))
	require.NoError(td, err)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(td.masterPrivateKey)

	_, _, txString, err := txb.BytesWithValidation()
	if err != nil {
		if prntx {
			td.Logf("error: %v\n--------- fialing tx -------\n%s", err, txString)
		}
		return err
	}
	if prntx {
		td.Logf("------------- valid tx ---\n%s", txString)
	}
	return nil
}

func TestDelegationLock2Consume(t *testing.T) {
	td := &testData{T: t}

	var err error
	var txString string
	_ = txString

	t.Run("init", func(t *testing.T) {
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(1)
		_, txString, err = td.initDelegationUTXOMake(ts, 4)
		require.NoError(t, err)
		//td.Logf("---------------- transaction -----------------\n%s", txString)
	})
	t.Run("master+init+kill", func(t *testing.T) {
		// create delegation output and destroy it with next tx
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024)
		require.NoError(t, err)
		//td.Logf("---------------- transaction -----------------\n%s", txString)

		ts = td.delegatedOutput.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		err = td.discontinueDelegation(ts, true)
		require.NoError(t, err)
	})
	t.Run("target+init", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024)
		require.NoError(t, err)

		err = td.transitChainWithDelegation(1, transitParams{
			ts:              td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
			delegationState: td.delegatedOutput.DelegateLock2State,
			prntx:           false,
		})
		require.NoError(t, err)
	})
	t.Run("target_test_safe_revocation_window", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 512)
		require.NoError(t, err)

		err = td.transitChainWithDelegation(1, transitParams{
			ts:              td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
			delegationState: td.delegatedOutput.DelegateLock2State,
			prntx:           false,
		})
		require.NoError(t, err)

		from, to := td.delegatedOutput.SafeRevocationSlots()
		td.Logf("safe revocation from=%d, to=%d", from, to)

		err = td.transitChainWithDelegation(2, transitParams{
			ts: base.MaximumTime(
				td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
				base.NewLedgerTime(from, 5),
			),
			prntx: false,
		})
		util.RequireErrorWith(t, err, "delegation target should not be unlocked inside safe revocation window")

		err = td.transitChainWithDelegation(3, transitParams{
			ts: base.MaximumTime(
				td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
				base.NewLedgerTime(to, 5),
			),
			prntx: false,
		})
		util.RequireErrorWith(t, err, "delegation target should not be unlocked inside safe revocation window")

		err = td.transitChainWithDelegation(4, transitParams{
			ts: base.MaximumTime(
				td.timestampTicksForward(int(ledger.L().ID.TransactionPace)),
				base.NewLedgerTime(to+1, 5),
			),
			prntx: false,
		})
		require.NoError(t, err)
	})
	t.Run("target_freeze_fail", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 512)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		err = td.transitChainWithDelegation(1, transitParams{
			ts: ts,
			delegationState: ledger.DelegateLock2State{
				UnfreezeSlot: ts.Slot + 512,
			},
			prntx: true,
		})
		util.RequireErrorWith(t, err, "unfreeze slot cannot exceed maximum set by delegator")
	})
	t.Run("target_freeze_ok", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 512)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		err = td.transitChainWithDelegation(1, transitParams{
			ts: ts,
			delegationState: ledger.DelegateLock2State{
				UnfreezeSlot: ts.Slot + 512 - 1,
			},
			prntx: false,
		})
		require.NoError(t, err)
	})
	t.Run("master_unlock_frozen", func(t *testing.T) {
		// target consumes initial delegation
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 512)
		require.NoError(t, err)

		ts = td.timestampTicksForward(int(ledger.L().ID.TransactionPace))
		err = td.transitChainWithDelegation(1, transitParams{
			ts: ts,
			delegationState: ledger.DelegateLock2State{
				UnfreezeSlot: ts.Slot + 512 - 1,
			},
			prntx: false,
		})
		require.NoError(t, err)

		ts = base.NewLedgerTime(td.delegatedOutput.UnfreezeSlot-100, 5)
		err = td.discontinueDelegation(ts, false)
		util.RequireErrorWith(t, err, "master can only unlock revoked or unfrozen")

		ts = base.NewLedgerTime(td.delegatedOutput.UnfreezeSlot-1, 5)
		err = td.discontinueDelegation(ts, false)
		util.RequireErrorWith(t, err, "master can only unlock revoked or unfrozen")

		ts = base.NewLedgerTime(td.delegatedOutput.UnfreezeSlot, 5)
		err = td.discontinueDelegation(ts, false)
		require.NoError(t, err)
	})
}
