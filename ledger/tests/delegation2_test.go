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

	initDelegationUTXO := func(ts base.LedgerTime, maxFreezeEpochs byte, startSlot base.Slot, startAmount uint64) error {
		// create delegation output
		par, err := u.MakeTransferInputData(masterPrivateKey, nil, ts)
		if err != nil {
			return err
		}

		delegationLock = ledger.NewDelegateToSequencerLock(target, masterAddr, maxFreezeEpochs, startSlot, startAmount)
		txBytes, err = txbuilder.MakeSimpleTransferTransaction(par.
			WithAmount(delegatedTokens).
			WithTargetLock(delegationLock).
			WithConstraint(ledger.NewChainOrigin()).
			WithConstraint(ledger.DelegateToSequencerLockState{}),
		)
		if err != nil {
			return err
		}

		t.Logf("============ transaction ==============\n%s", u.TxToSource(txBytes))

		err = u.AddTransaction(txBytes)
		if err != nil {
			t.Logf("============ failing transaction ==============\n%s", u.TxToSource(txBytes))
			return err
		}
		t.Logf("delegation ID: %s", delegationID.String())
		return nil
	}

	t.Run("init ok", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		err := initDelegationUTXO(ts, 1, ts.Slot, delegatedTokens)
		require.NoError(t, err)
	})
	t.Run("init fail 1", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		err := initDelegationUTXO(ts, 1, ts.Slot+1, delegatedTokens)
		util.RequireErrorWith(t, err, "wrong start slot")
	})
	t.Run("init fail 2", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		err := initDelegationUTXO(ts, 1, ts.Slot, delegatedTokens-1)
		util.RequireErrorWith(t, err, "wrong start amount")
	})
	t.Run("init ok 2", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		err := initDelegationUTXO(ts, ledger.DelegationMaxFreezeEpochs(), ts.Slot, delegatedTokens)
		require.NoError(t, err)
	})
	t.Run("init fail 3", func(t *testing.T) {
		initBase()
		ts := chainOrigin.Timestamp().AddTicks(1)
		err := initDelegationUTXO(ts, ledger.DelegationMaxFreezeEpochs()+1, ts.Slot, delegatedTokens)
		util.RequireErrorWith(t, err, "wrong max freeze epochs")
	})
}
