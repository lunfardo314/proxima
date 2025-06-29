package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ed25519"
)

func TestDelegationLock2(t *testing.T) {
	require.EqualValues(t, 512, ledger.DelegationEpochSlots())
	require.EqualValues(t, 24, ledger.DelegationSafeRevocationSlots())
	require.True(t, ledger.DelegationSafeRevocationSlots() < ledger.DelegationEpochSlots())
	// require safe revocation window to be up to 5% of the epoch
	require.True(t, ledger.DelegationSafeRevocationSlots()/ledger.DelegationEpochSlots() < 5)

	var u *utxodb.UTXODB
	var target ledger.ChainLock
	var masterAddr ledger.AddressED25519

	var seqPrivateKey, masterPrivateKey ed25519.PrivateKey

	const (
		tokensFromFaucetMaster        = 200_000_000_000
		tokensFromFaucetSeqController = 200_000_000_000
		seqOnChainBalance             = 199_999_000_000
		delegatedTokens               = 1_000_000_000
	)
	var delegationLock *ledger.DelegateToSequencerLock
	var txBytes []byte
	var chainOrigin, delegatedOutput *ledger.OutputWithChainID
	var seqID, delegationID base.ChainID

	_, _, _, _ = delegationLock, txBytes, target, delegatedOutput

	initBase := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)

		privKey, _, addr := u.GenerateAddresses(0, 2)
		masterPrivateKey = privKey[0]
		masterAddr = addr[0]
		seqPrivateKey = privKey[1]
		seqControllerAddr := addr[1]

		err := u.TokensFromFaucet(masterAddr, tokensFromFaucetMaster)
		require.NoError(t, err)
		err = u.TokensFromFaucet(seqControllerAddr, tokensFromFaucetSeqController)
		require.NoError(t, err)

		// create chain for sequencer
		par, err := u.MakeTransferInputData(seqPrivateKey, nil, base.NilLedgerTime)
		require.NoError(t, err)
		outs, err := u.DoTransferOutputs(par.
			WithAmount(seqOnChainBalance).
			WithTargetLock(seqControllerAddr).
			WithConstraint(ledger.NewChainOrigin()),
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, len(outs))
		chOuts, err := ledger.FilterChainOutputs(outs)
		require.NoError(t, err)
		require.EqualValues(t, 1, len(chOuts))

		chainOrigin = chOuts[0]
		var ok bool
		seqID, _, ok = chainOrigin.ExtractChainID()
		require.True(t, ok)
		t.Logf("seq chain origin:\n%s", chainOrigin.String())

		target = ledger.ChainLockFromChainID(seqID)
		t.Logf("==== master address    : %s (%s)", masterAddr.String(), util.Th(u.Balance(masterAddr)))
		t.Logf("==== seq controller    : %s (%s)", seqControllerAddr.String(), util.Th(u.Balance(seqControllerAddr)))
		_, onChain, err := u.BalanceOnChain(seqID)
		require.NoError(t, err)
		t.Logf("==== seq on-chain      : %s", util.Th(onChain))
		t.Logf("==== delegation target : %s (%s)", target.String(), util.Th(u.Balance(target)))

		// create seq transaction
		dummySeqTxID := base.NewTransactionID(base.LedgerTime{Slot: chainOrigin.Timestamp().Slot}, base.TransactionIDShort{}, true)
		tsSeq := chOuts[0].Timestamp().AddTicks(int(ledger.L().ID.TransactionPaceSequencer))
		txBytes, err = txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			PrivateKey:   seqPrivateKey,
			SeqName:      "testSeq",
			ChainInput:   chainOrigin,
			Timestamp:    tsSeq,
			Endorsements: []base.TransactionID{dummySeqTxID},
		})
		require.NoError(t, err)
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)
	}

	initDelegationUTXO := func(ts base.LedgerTime, revoked bool, maxFreezeEpochs byte, startSlot base.Slot, startAmount uint64, prnOnError bool) ([]byte, error) {
		// create delegation output
		par, err := u.MakeTransferInputData(masterPrivateKey, nil, ts)
		if err != nil {
			return nil, err
		}

		delegationLock = ledger.NewDelegateToSequencerLock(target, masterAddr, maxFreezeEpochs, startSlot, startAmount)
		txBytes, err = txbuilder.MakeSimpleTransferTransaction(par.
			WithAmount(delegatedTokens).
			WithTargetLock(delegationLock).
			WithConstraint(ledger.NewChainOrigin()).
			WithConstraint(ledger.DelegateToSequencerLockState{Revoked: revoked}),
		)
		if err != nil && prnOnError {
			t.Logf(">>>>> %v\n============ transaction ==============\n%s", err, u.TxToSource(txBytes))
			return nil, err
		}
		if err = u.AddTransaction(txBytes); err == nil {
			outs, err := u.SugaredStateReader().GetOutputsDelegatedToAccount2(target)
			require.NoError(t, err)
			require.EqualValues(t, 1, len(outs))
			delegatedOutput = outs[0]
			delegationID = delegatedOutput.ChainID
			t.Logf("delegation ID: %s", delegationID.String())
			t.Logf("delegated UTXO:\n%s", delegatedOutput.Output.ToSource("     "))
		}
		return txBytes, err
	}

	var err error
	t.Run("init ok", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		txBytes, err = initDelegationUTXO(ts, false, 1, ts.Slot, delegatedTokens, true)
		require.NoError(t, err)
	})
	t.Run("init ok 2", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		txBytes, err = initDelegationUTXO(ts, false, ledger.DelegationMaxFreezeEpochs(), ts.Slot, delegatedTokens, true)
		require.NoError(t, err)
	})
	t.Run("init fail 1", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		_, err = initDelegationUTXO(ts, false, 1, ts.Slot+1, delegatedTokens, false)
		util.RequireErrorWith(t, err, "wrong start parameters")
	})
	t.Run("init fail 2", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		_, err = initDelegationUTXO(ts, false, 1, ts.Slot, delegatedTokens-1, false)
		util.RequireErrorWith(t, err, "wrong start parameters")
	})
	t.Run("init fail 3", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		_, err = initDelegationUTXO(ts, true, 1, ts.Slot, delegatedTokens, true)
		util.RequireErrorWith(t, err, "wrong start parameters")
	})
	t.Run("init fail 4", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		_, err = initDelegationUTXO(ts, false, ledger.DelegationMaxFreezeEpochs()+1, ts.Slot, delegatedTokens, true)
		util.RequireErrorWith(t, err, "wrong max freeze epochs")
	})
	t.Run("init fail 5", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		_, err = initDelegationUTXO(ts, false, ledger.DelegationMaxFreezeEpochs()+1, ts.Slot, delegatedTokens, true)
		util.RequireErrorWith(t, err, "wrong max freeze epochs")
	})
}
