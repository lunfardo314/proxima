package tests

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/stretchr/testify/require"
)

// The reception gate on bootstrap transactions mirrors the issuing condition: an unsolicited
// bootstrap transaction (explicit baseline) is attached only when this node's own latest
// reliable branch also lags the transaction's slot by the bootstrap lag, i.e. this node sees the
// network as stuck too. Otherwise the sender's LRB is frozen while the network is branching, and
// the transaction is ignored (never invalidated). The two tests below gossip a hand-built
// bootstrap transaction into a workflow in each of the two states.

// gossipBootstrapTx builds a bootstrap transaction of the bootstrap sequencer anchored on the
// given branch — the chain output as committed in that branch, the branch as explicit baseline,
// no endorsements — timestamped in the current slot, and feeds it to the workflow as if received
// from a peer. It returns the transaction ID. The caller makes sure the branch is in a past slot,
// as the ledger requires of an explicit baseline.
func gossipBootstrapTx(t *testing.T, td *workflowTestData, baseline *multistate.BranchData) base.TransactionID {
	lrbID := baseline.Stem.ID.TransactionID()
	require.Less(t, lrbID.Slot(), ledger.TimeNow().Slot)

	chainOut, err := td.wrk.Branches().GetChainOutputFromBranch(lrbID, td.bootstrapChainID)
	require.NoError(t, err)
	chainIn, ok := ledger.AsOutputWithChainID(chainOut.Output, chainOut.ID)
	require.True(t, ok)

	txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:          "stale",
		Timestamp:        pastTimestamp(chainOut.Timestamp()),
		ChainInput:       &chainIn,
		ExplicitBaseline: &lrbID,
		SignatureType:    base.SignatureTypeED25519,
		PrivateKey:       genesisPrivateKey,
		PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
	})
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	_, isBootstrap := tx.ExplicitBaseline()
	require.True(t, isBootstrap)

	td.wrk.TxBytesInFromPeerQueued(txBytes, nil, peer.ID("some peer"), tx.ID())
	return tx.ID()
}

// pastTimestamp returns a ledger time a couple of ticks in the past of the current slot, so the
// transaction is never deferred by clock alignment, and at least the sequencer pace after the
// chain input. It waits for the slot to be old enough rather than stepping back into the previous
// slot, which the gate would judge differently.
func pastTimestamp(chainInputTs base.LedgerTime) base.LedgerTime {
	for {
		now := ledger.TimeNow()
		if now.Tick >= 4 {
			ts := base.T(now.Slot, now.Tick-2)
			if ledger.ValidSequencerPace(chainInputTs, ts) {
				return ts
			}
		}
		time.Sleep(ledger.TickDuration())
	}
}

// TestBootstrapTxIgnoredWhileBranching: a sequencer keeps the node's LRB current, so a gossiped
// bootstrap transaction anchored on that LRB is dropped by the reception gate and never reaches
// the memDAG.
func TestBootstrapTxIgnoredWhileBranching(t *testing.T) {
	td := initWorkflowTest(t, 1)

	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithMaxBranches(8))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		td.stop()
	})
	seq.Start()

	// wait until branches are being produced: the LRB is within the bootstrap lag of now
	deadline := time.Now().Add(8 * ledger.SlotDuration())
	var lrb *multistate.BranchData
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) &&
			lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
			break
		}
		require.True(t, time.Now().Before(deadline), "the sequencer did not bring the LRB up to date")
		time.Sleep(ledger.TickDuration())
	}
	// the explicit baseline must be in a past slot: anchor on this LRB from the next slot on.
	// The LRB at reception is then this branch or a newer one, either way within the lag.
	for ledger.TimeNow().Slot <= lrb.Stem.ID.Slot() {
		time.Sleep(ledger.TickDuration())
	}

	txid := gossipBootstrapTx(t, td, lrb)
	time.Sleep(ledger.SlotDuration())

	require.EqualValues(t, 1, td.wrk.Counter("bootstrap_drop"))
	require.Nil(t, td.wrk.GetVertex(txid), "an ignored bootstrap transaction must not be attached")
	// ignored is not invalidated: the transaction bytes are kept, so it can still be pulled
	// if some branch's past cone turns out to need it
	require.True(t, td.wrk.TxBytesStore().HasTxBytes(&txid))

	td.waitStop()
}

// TestBootstrapTxAttachedWhileStuck: no sequencer runs, the LRB stays at the distribution branch
// and falls behind by more than the bootstrap lag. A gossiped bootstrap transaction is then in
// order and attaches.
func TestBootstrapTxAttachedWhileStuck(t *testing.T) {
	td := initWorkflowTest(t, 1)

	lrb := td.wrk.Branches().FindLatestReliableBranch()
	require.NotNil(t, lrb)
	// let the LRB fall behind by the bootstrap lag
	for !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) {
		time.Sleep(ledger.TickDuration())
	}

	txid := gossipBootstrapTx(t, td, lrb)
	time.Sleep(ledger.SlotDuration())

	require.EqualValues(t, 0, td.wrk.Counter("bootstrap_drop"))
	require.NotNil(t, td.wrk.GetVertex(txid), "a bootstrap transaction in a stuck network must be attached")

	td.stop()
	td.waitStop()
}
