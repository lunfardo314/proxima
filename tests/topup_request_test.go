// The top-up request end to end through the sequencer's builder
// (kb/delegation_topup.md): the master posts a tag-along carrying the amount
// and an ensureTopUpDelegation, the sequencer picks it up as a tag-along
// input, and the delegation grows in place, frozen or not. Runs over the
// utxodb harness of txbuilder_seq_test.go, so every transaction is validated
// by the ledger.
package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// seqStep builds one milestone at ts: freezes what is freezable and offers
// every backlog output as a tag-along, reporting the fate of each.
func (td *testWithUTXODBData) seqStep(t *testing.T, ts base.LedgerTime) (consumed, refusedForNow, refusedForGood []base.OutputID) {
	rdr := td.u.SugaredStateReader()
	txb, err := txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, rdr)
	require.NoError(t, err)
	require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
	for _, o := range td.tagAlongBacklog() {
		_, valid, err := txb.AddTagAlongInput(o)
		switch {
		case err == nil:
			consumed = append(consumed, o.ID)
		case valid:
			t.Logf("   %s temporarily refused: %v", o.ID.StringShort(), err)
			refusedForNow = append(refusedForNow, o.ID)
		default:
			t.Logf("   %s permanently refused: %v", o.ID.StringShort(), err)
			refusedForGood = append(refusedForGood, o.ID)
		}
	}
	for _, dIn := range td.freezableDelegations(ts) {
		if txb.IsConsumed(dIn.ID) {
			continue
		}
		_, _, err = txb.FreezeDelegation(&dIn)
		require.NoError(t, err)
	}
	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "milestone must validate:\n%s", txString)
	require.NoError(t, td.u.AddTransaction(txBytes))
	return
}

func (td *testWithUTXODBData) postTopUp(t *testing.T, i int, amount uint64, ts base.LedgerTime) base.OutputID {
	out := txbuilder_seq.NewTopUpDelegationReqOutput(td.seqID, td.masterAddr, td.delegationIDs[i], amount)
	require.NoError(t, td.u.SendOutput(td.masterPrivateKey, out, ts))
	backlog := td.tagAlongBacklog()
	require.NotEmpty(t, backlog)
	return backlog[len(backlog)-1].ID
}

// walletLibFromGlobalTests builds the wallet's library from the singleton via
// JSON, as the wallet does at init.
func walletLibFromGlobalTests(t *testing.T) *txbuildercore.Library[any] {
	t.Helper()
	desc, err := easyfl.ReadLibraryFromJSON(easyfl.ToJSON(ledger.L(base.MaxSlot).Library, true, false))
	require.NoError(t, err)
	tlib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)
	return tlib
}

func (td *testWithUTXODBData) delegation(t *testing.T, i int) ledger.DelegationOutput {
	d, err := td.u.SugaredStateReader().GetDelegatedOutput(td.delegationIDs[i])
	require.NoError(t, err)
	return d
}

func TestTopUpRequestThroughSequencer(t *testing.T) {
	const amount = 2 * txbuildercore.MinimumTopUpAmount
	td, ts := newTestWithUTXODBData(t, 2)

	// first milestone freezes both delegations, one slot after they were created
	d1 := td.delegation(t, 1)
	ts = base.MaximumTime(ts, d1.ID.Timestamp()).AddSlots(1)
	td.seqStep(t, ts)
	d0 := td.delegation(t, 0)
	require.True(t, d0.IsMarkedFrozen())
	seqBefore, err := td.u.SugaredStateReader().GetChainOutputWithID(td.seqID)
	require.NoError(t, err)

	// a top-up of the frozen delegation 0 and one under the minimum
	ts = ts.AddSlots(1)
	ok := td.postTopUp(t, 0, amount, ts)
	// a second transaction from the same wallet needs the ledger pace between them
	tooSmall := td.postTopUp(t, 1, txbuildercore.MinimumTopUpAmount-1, ts.AddTicks(int(ledger.L(0).TransactionPace)))

	ts = ts.AddSlots(1)
	consumed, _, refusedForGood := td.seqStep(t, ts)
	require.Contains(t, consumed, ok)
	require.Contains(t, refusedForGood, tooSmall)

	after := td.delegation(t, 0)
	require.True(t, after.IsMarkedFrozen())
	require.Equal(t, d0.LastFrozenEpoch, after.LastFrozenEpoch)
	require.Equal(t, d0.AdvanceShare, after.AdvanceShare)
	_, _, frozenEpochs := d0.FrozenEpochs(ts)
	advance := d0.AdvanceOnAmount(amount, ts, frozenEpochs, d0.AdvanceShare)
	require.EqualValues(t, d0.Output.TokenBalance()+d0.InflationOneSlot()+amount+advance, after.Output.TokenBalance())
	seqAfter, err := td.u.SugaredStateReader().GetChainOutputWithID(td.seqID)
	require.NoError(t, err)
	// the sequencer paid the advance and gained the increase as frozen coverage
	require.EqualValues(t, seqBefore.Output.FrozenCoverage(0)+int64(after.Output.TokenBalance()-d0.Output.TokenBalance()), seqAfter.Output.FrozenCoverage(0))

	// the too-small request is the master's again once the window closes
	lib := ledger.L(base.MaxSlot)
	wlib := walletLibFromGlobalTests(t)
	reqOut, err := td.u.SugaredStateReader().GetOutputWithID(tooSmall)
	require.NoError(t, err)
	txBytes, _, _, err := txbuildercore.MakeCompactTransaction(wlib, lib.Constants, txbuildercore.CompactParams{
		Inputs:           []txbuildercore.CompactInput{{OutputBytes: reqOut.Output.Bytes(), ID: reqOut.ID}},
		WalletPrivateKey: td.masterPrivateKey,
		TagAlongSeqID:    td.seqID,
		TagAlongFee:      500,
		TargetSlot:       tooSmall.Slot() + lib.TagAlongSlots,
	})
	require.NoError(t, err)
	require.NoError(t, td.u.AddTransaction(txBytes))
}

// A top-up of an unfrozen delegation freezes it in the same milestone with
// the amount included, placed by the freeze pass's epoch choice.
func TestTopUpRequestFreshFreezeThroughSequencer(t *testing.T) {
	const amount = 3 * txbuildercore.MinimumTopUpAmount
	td, ts := newTestWithUTXODBData(t, 1)
	d0 := td.delegation(t, 0)
	require.False(t, d0.IsMarkedFrozen())

	ts = base.MaximumTime(ts, d0.ID.Timestamp()).AddSlots(1)
	req := td.postTopUp(t, 0, amount, ts)
	ts = ts.AddSlots(1)
	consumed, _, _ := td.seqStep(t, ts)
	require.Contains(t, consumed, req)

	after := td.delegation(t, 0)
	require.True(t, after.IsMarkedFrozen())
	require.Greater(t, after.Output.TokenBalance(), d0.Output.TokenBalance()+amount)
	seqOut, err := td.u.SugaredStateReader().GetChainOutputWithID(td.seqID)
	require.NoError(t, err)
	require.EqualValues(t, after.Output.TokenBalance(), seqOut.Output.FrozenCoverage(0))
}
