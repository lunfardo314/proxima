package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// The target of a delegation unlocks it only in its own milestone: the frozen coverage a freeze or a
// hold moves is booked on the sequencer output of the transaction, so that output must be the
// target's. Otherwise a delegation aimed at a plain chain T could be frozen in a milestone of another
// sequencer S, crediting S, and put on hold in an ordinary transaction, debiting nobody.
//
// A sequencer S and a plain chain T of the same key, and delegations to T. Before upgrade 7 the
// ledger accepts the freeze in S's milestone and the node check CheckStemAndTargetUnlocks refuses it,
// and the hold in an ordinary transaction; from upgrade 7 the ledger refuses both.
func TestDelegationTargetUnlocksOnlyInItsMilestone(t *testing.T) {
	const (
		seqOwn   = 3_000 * base.PROX
		tBalance = 25_000 * base.PROX
		delegAmt = 50_000 * base.PROX
		refused  = "delegation target unlocks only in its own milestone"
	)
	saved := transaction.StemTargetCheckFromSlot
	transaction.StemTargetCheckFromSlot = 0
	t.Cleanup(func() { transaction.StemTargetCheckFromSlot = saved })

	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	keys, _, addrs := u.GenerateAddresses(0, 1)
	key, addr := keys[0], addrs[0]
	require.NoError(t, u.TokensFromFaucet(addr, 200_000*base.PROX))
	seq := mustMakeSequencerChainOrigin(t, u, key, addr, seqOwn, true)
	withUpgrade7(t, u)
	tOrig, err := u.CreateChainOrigin(key, base.T(u7Slot-50, 40), tBalance)
	require.NoError(t, err)
	tChain := tOrig.ChainID

	chainOut := func(id base.ChainID) *ledger.OutputWithChainID {
		o, err := u.SugaredStateReader().GetChainOutputWithChainID(id)
		require.NoError(t, err)
		return &o
	}
	succCC := func(c *ledger.OutputWithChainID, predIdx byte) *ledger.ChainConstraint {
		return ledger.NewChainConstraint(c.ChainID, predIdx, c.OriginSlot, c.CumulativeChainInflation, c.CumulativeBranchBonus, c.TransitionCounter+1, c.BranchCounter)
	}
	// validate parses and validates the transaction in full context; the node check runs on it too
	validate := func(txb *exhelp.Builder) (*transaction.Transaction, []byte, error) {
		txBytes := txb.Bytes()
		tx, err := transaction.ParseAndValidate(txBytes, txb.LoadInputBytes)
		return tx, txBytes, err
	}

	// a delegation of the key to T, cut 0
	newDelegation := func(ts base.LedgerTime) ledger.DelegationOutput {
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		var src *ledger.OutputWithID
		for _, o := range outs {
			if o.Output.Lock().Name() == ledger.SigLockName && o.Output.ChainConstraint() == nil && o.Output.TokenBalance() >= delegAmt+base.PROX {
				src = o
				break
			}
		}
		require.NotNil(t, src)
		txb := exhelp.New()
		_, err = txb.ConsumeOutput(src.Output, src.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		dIdx, err := txb.ProduceOutput(ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
			Amount: delegAmt, MasterID: base.HolderID(addr), Target: tChain, RequiredInflationCut: 0, StartSlot: ts.Slot,
		}))
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(src.Output.TokenBalance() - delegAmt).WithLock(addr)
		}))
		require.NoError(t, err)
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(key)
		tx, txBytes, err := validate(txb)
		require.NoError(t, err)
		require.NoError(t, u.AddTransaction(txBytes))
		d, ok := ledger.AsDelegationOutput(tx.MustProducedOutputWithIDAt(dIdx).Output, tx.OutputID(dIdx))
		require.True(t, ok)
		return d
	}

	// a milestone of S consuming T and the delegation, freezing it as unlocked by T
	freeze := func(d ledger.DelegationOutput, ts base.LedgerTime) (*transaction.Transaction, []byte, error) {
		s, tc := chainOut(seq.ChainID), chainOut(tChain)
		txb := exhelp.New()
		_, _, err := txb.ConsumeOutputsNoUnlock(&s.OutputWithID, &tc.OutputWithID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		require.NoError(t, txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0))
		sIdx, err := txb.ProduceOutput(s.Output.Clone(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(s.Output.TokenBalance()))
			o.PutConstraint(succCC(s, 0).Bytes(), ledger.ConstraintIndexChain)
		}))
		require.NoError(t, err)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(sIdx))
		tIdx, err := txb.ProduceOutput(tc.Output.Clone(func(o *ledger.OutputBuilder) {
			o.PutConstraint(succCC(tc, 1).Bytes(), ledger.ConstraintIndexChain)
		}))
		require.NoError(t, err)
		txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(tIdx))
		dIn, err := txb.ConsumeOutput(d.Output, d.ID)
		require.NoError(t, err)
		txb.PutUnlockParams(dIn, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(1), ledger.DelegationUnlockedByTarget)
		dSucc, err := d.MakeDelegationFreezeOutput(ts, d.FreezeUntilMax(ts), dIn, 0, true)
		require.NoError(t, err)
		dOutIdx, err := txb.ProduceOutput(dSucc)
		require.NoError(t, err)
		txb.PutUnlockParams(dIn, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(dOutIdx))
		fc, err := txb.CalcFrozenCoverageDelta()
		require.NoError(t, err)
		txb.MustPutFrozenCoverage(sIdx, fc, ts)
		txb.SetSequencerData(sIdx, txbuildercore.SequencerOutputIndexNone)
		txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))
		txb.ComputeInputCommitment()
		txb.SetTimestamp(ts)
		txb.SignED25519(key)
		return validate(txb)
	}

	// an ordinary transaction consuming T and the frozen delegation, putting it on hold as unlocked by T
	hold := func(d ledger.DelegationOutput, ts base.LedgerTime) (*transaction.Transaction, []byte, error) {
		tc := chainOut(tChain)
		txb := exhelp.New()
		_, _, err := txb.ConsumeOutputsNoUnlock(&tc.OutputWithID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		tIdx, err := txb.ProduceOutput(tc.Output.Clone(func(o *ledger.OutputBuilder) {
			o.PutConstraint(succCC(tc, 0).Bytes(), ledger.ConstraintIndexChain)
		}))
		require.NoError(t, err)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(tIdx))
		dIn, err := txb.ConsumeOutput(d.Output, d.ID)
		require.NoError(t, err)
		txb.PutUnlockParams(dIn, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), ledger.DelegationUnlockedByTarget)
		onHold, err := d.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{TxTs: ts, PredOutputIndex: dIn, DisableConsistencyChecks: true})
		require.NoError(t, err)
		dOutIdx, err := txb.ProduceOutput(onHold)
		require.NoError(t, err)
		txb.PutUnlockParams(dIn, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(dOutIdx))
		txb.ComputeInputCommitment()
		txb.SetTimestamp(ts)
		txb.SignED25519(key)
		return validate(txb)
	}

	// before the slot: the ledger accepts the freeze in S's milestone, the node check refuses it
	d1 := newDelegation(base.T(u7Slot-45, 40))
	tx, txBytes, err := freeze(d1, base.T(u7Slot-43, 40))
	require.NoError(t, err)
	err = tx.CheckStemAndTargetUnlocks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "in another sequencer's milestone")
	require.NoError(t, u.AddTransaction(txBytes)) // what a node without the check would do

	frozen, err := u.SugaredStateReader().GetDelegatedOutput(d1.ChainID)
	require.NoError(t, err)
	// before the slot: the hold in an ordinary transaction, refused by the node check
	tx, _, err = hold(frozen, base.T(u7Slot-41, 40))
	require.NoError(t, err)
	err = tx.CheckStemAndTargetUnlocks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside a milestone")
	// from the slot: the same hold refused by the ledger
	_, _, err = hold(frozen, base.T(u7Slot+2, 40))
	require.Error(t, err)
	require.Contains(t, err.Error(), refused)

	// from the slot: a fresh freeze in S's milestone refused by the ledger
	d2 := newDelegation(base.T(u7Slot+3, 40))
	_, _, err = freeze(d2, base.T(u7Slot+5, 40))
	require.Error(t, err)
	require.Contains(t, err.Error(), refused)
}
