package tests

import (
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
	delegationLock                  *ledger.DelegateToSequencerLock
	seqChainOrigin, delegatedOutput *ledger.OutputWithChainID
	seqID, delegationID             base.ChainID
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
	par, err := td.u.MakeTransferInputData(td.seqPrivateKey, nil, base.NilLedgerTime)
	require.NoError(td, err)
	outs, err := td.u.DoTransferOutputs(par.
		WithAmount(seqOnChainBalance).
		WithTargetLock(seqControllerAddr).
		WithConstraint(ledger.NewChainOrigin()),
	)
	require.NoError(td, err)
	require.EqualValues(td, 2, len(outs))
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(td, err)
	require.EqualValues(td, 1, len(chOuts))

	td.seqChainOrigin = chOuts[0]
	var ok bool
	td.seqID, _, ok = td.seqChainOrigin.ExtractChainID()
	require.True(td, ok)
	td.Logf("seq chain origin:\n%s", td.seqChainOrigin.String())

	td.target = ledger.ChainLockFromChainID(td.seqID)
	td.Logf("==== master address    : %s (%s)", td.masterAddr.String(), util.Th(td.u.Balance(td.masterAddr)))
	td.Logf("==== seq controller    : %s (%s)", seqControllerAddr.String(), util.Th(td.u.Balance(seqControllerAddr)))
	_, onChain, err := td.u.BalanceOnChain(td.seqID)
	require.NoError(td, err)
	td.Logf("==== seq on-chain      : %s", util.Th(onChain))
	td.Logf("==== delegation target : %s (%s)", td.target.String(), util.Th(td.u.Balance(td.target)))

	// create seq transaction
	dummySeqTxID := base.NewTransactionID(base.LedgerTime{Slot: td.seqChainOrigin.Timestamp().Slot}, base.TransactionIDShort{}, true)
	tsSeq := chOuts[0].Timestamp().AddTicks(int(ledger.L().ID.TransactionPaceSequencer))
	var txBytes []byte
	txBytes, err = txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		PrivateKey:   td.seqPrivateKey,
		SeqName:      "testSeq",
		ChainInput:   td.seqChainOrigin,
		Timestamp:    tsSeq,
		Endorsements: []base.TransactionID{dummySeqTxID},
	})
	require.NoError(td, err)

	err = td.u.AddTransaction(txBytes)
	require.NoError(td, err)

	seqOut, err := transaction.OutputWithIDFromTransactionBytes(txBytes, 0)
	require.NoError(td, err)

	td.seqChainOrigin, err = seqOut.AsChainOutput()
	require.NoError(td, err)

	require.EqualValues(td, td.seqID, td.seqChainOrigin.ChainID)
}

func (td *testData) initDelegationUTXODirect(ts base.LedgerTime, revoked bool, maxFreezeSlots uint16, prnOnError bool) ([]byte, error) {
	var txBytes []byte

	// create delegation output
	par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
	if err != nil {
		return nil, err
	}

	td.delegationLock = ledger.NewDelegateToSequencerLock(td.target, td.masterAddr, maxFreezeSlots)
	txBytes, err = txbuilder.MakeSimpleTransferTransaction(par.
		WithAmount(delegatedTokens).
		WithTargetLock(td.delegationLock).
		WithConstraint(ledger.NewChainOrigin()).
		WithConstraint(ledger.DelegateToSequencerLockState{Revoked: revoked}),
	)
	if err != nil && prnOnError {
		td.Logf(">>>>> %v\n============ transaction ==============\n%s", err, td.u.TxToSource(txBytes))
		return nil, err
	}
	if err = td.u.AddTransaction(txBytes); err == nil {
		outs, err := td.u.SugaredStateReader().GetOutputsDelegatedToAccount2(td.target)
		require.NoError(td, err)
		require.EqualValues(td, 1, len(outs))
		td.delegatedOutput = outs[0]
		td.delegationID = td.delegatedOutput.ChainID
		td.Logf("delegation ID: %s", td.delegationID.String())
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
	td.delegatedOutput = dc
	td.delegationID = dc.ChainID

	err = td.u.AddTransaction(txBytes)
	return txBytes, txString, err

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

		txb := txbuilder.New()
		amount, ts, err := txb.ConsumeOutputsNoUnlock(&td.delegatedOutput.OutputWithID)
		require.NoError(t, err)

		txb.PutUnlockParams(0, 2, ledger.EndChainUnlockParams)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmount(amount - tagAlongFee).WithLock(td.masterAddr)
		}))
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmount(tagAlongFee).WithLock(ledger.ChainLockFromChainID(base.RandomChainID()))
		}))
		require.NoError(t, err)

		ts = ts.AddTicks(int(ledger.L().ID.TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(int(ledger.L().ID.PostBranchConsolidationTicks))
		}
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.TransactionData.Timestamp = ts
		txb.SignED25519(td.masterPrivateKey)

		_, _, txString, err := txb.BytesWithValidation()
		if err != nil {
			t.Logf(">>>> %v\n----------------\n%s", err, txString)
		} else {
			t.Logf("----------------\n%s", txString)
		}
		require.NoError(t, err)
	})
	t.Run("target+init+repeat", func(t *testing.T) {
		// create delegation output and destroy it with next tx
		td.init()
		ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L().ID.TransactionPace))
		_, txString, err = td.initDelegationUTXOMake(ts, 1024)
		require.NoError(t, err)
		//td.Logf("---------------- transaction -----------------\n%s", txString)

		txb := txbuilder.New()

		// transit sequencer
		_, _, err = txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
		require.NoError(t, err)

		successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, 2, 0)
		_, err = txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
			o.PutConstraint(successorChainConstraint.Bytes(), 2)
		}))
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, 2, ledger.NewChainUnlockParams(0, 2, 0))

		// transit delegation
		_, _, err = txb.ConsumeOutputsNoUnlock(&td.delegatedOutput.OutputWithID)
		require.NoError(t, err)

		err = txb.PutUnlockReference(1, 1, 0)
		txb.PutUnlockParams(1, 2, ledger.NewChainUnlockParams(1, 2, 0))

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmount(td.delegatedOutput.Output.Amount())
			o.WithLock(td.delegatedOutput.Output.Lock())
			o.MustPushConstraint(ledger.NewChainConstraint(td.delegationID, 1, 2, 0).Bytes())
			o.MustPushConstraint(td.delegatedOutput.Output.MustConstraintAt(3))
		}))
		require.NoError(t, err)

		ts = base.MaximumTime(td.seqChainOrigin.Timestamp(), td.delegatedOutput.Timestamp())

		ts = ts.AddTicks(int(ledger.L().ID.TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(int(ledger.L().ID.PostBranchConsolidationTicks))
		}
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.TransactionData.Timestamp = ts
		txb.SignED25519(td.seqPrivateKey)

		_, _, txString, err := txb.BytesWithValidation()
		if err != nil {
			t.Logf(">>>> %v\n----------------\n%s", err, txString)
		} else {
			t.Logf("----------------\n%s", txString)
		}
		require.NoError(t, err)
	})
}
