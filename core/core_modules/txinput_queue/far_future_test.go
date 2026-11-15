package txinput_queue

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestSlotsInTheFuture checks the ledger-tick distance that decides the hard future drop in
// processValidated: zero for anything not ahead of now, whole slots ahead otherwise, and no overflow for
// the largest slot value (the gossiped slot 1_400_000_000 of 2026-11-15 overflowed the clock conversion
// and crashed nodes on the attacher's ledger-time assertion).
func TestSlotsInTheFuture(t *testing.T) {
	now := base.LedgerTime{Slot: 236_298, Tick: 49}

	require.EqualValues(t, 0, slotsInTheFuture(now, now))
	require.EqualValues(t, 0, slotsInTheFuture(base.LedgerTime{Slot: 236_200, Tick: 3}, now))
	require.EqualValues(t, 0, slotsInTheFuture(base.LedgerTime{Slot: 236_299, Tick: 10}, now), "less than a slot ahead")
	require.EqualValues(t, 1, slotsInTheFuture(base.LedgerTime{Slot: 236_299, Tick: 49}, now))
	require.EqualValues(t, hardMaxSlotsInTheFuture, slotsInTheFuture(base.LedgerTime{Slot: 236_298 + hardMaxSlotsInTheFuture, Tick: 49}, now), "at the bound: still accepted")
	require.EqualValues(t, hardMaxSlotsInTheFuture, slotsInTheFuture(base.LedgerTime{Slot: 236_298 + hardMaxSlotsInTheFuture + 1, Tick: 0}, now), "whole slots only: a tick short of the next")
	require.Greater(t, slotsInTheFuture(base.LedgerTime{Slot: 236_298 + hardMaxSlotsInTheFuture + 1, Tick: 49}, now), int64(hardMaxSlotsInTheFuture))
	require.Greater(t, slotsInTheFuture(base.LedgerTime{Slot: 1_400_000_000, Tick: 0}, now), int64(hardMaxSlotsInTheFuture))
	require.Greater(t, slotsInTheFuture(base.LedgerTime{Slot: base.MaxSlot, Tick: base.MaxTickValue}, now), int64(hardMaxSlotsInTheFuture))
}

// farFutureTestEnvironment records what processValidated does with a transaction before the hard
// future gate drops it: nothing beyond the penalty, so persisting or gossiping fails the test.
type farFutureTestEnvironment struct {
	environment
	penalized []peer.ID
	persisted bool
	gossiped  bool
}

func (*farFutureTestEnvironment) Log() *zap.SugaredLogger { return zap.NewNop().Sugar() }
func (*farFutureTestEnvironment) SelfPeerID() peer.ID     { return "self" }
func (*farFutureTestEnvironment) IncCounter(string)       {}
func (e *farFutureTestEnvironment) EvidenceInvalidTxFromPeer(id peer.ID) {
	e.penalized = append(e.penalized, id)
}
func (e *farFutureTestEnvironment) MustPersistTxBytes(*transaction.Transaction) { e.persisted = true }
func (e *farFutureTestEnvironment) GossipTxBytesToPeers([]byte, base.TransactionID, ...peer.ID) {
	e.gossiped = true
}

// TestFarFutureTransactionDropped feeds processValidated a well-formed transaction stamped slot
// 1_400_000_000, the one gossiped on 2026-11-15. Whatever the source, it must be dropped before it is
// persisted, gossiped or attached, and the peer that sent it is penalized; the clock conversion of the
// timestamp must land in the future, not overflow into the past.
func TestFarFutureTransactionDropped(t *testing.T) {
	key := ledger.InitWithTestingLedgerData()
	t.Cleanup(ledger.ResetForTesting)
	output := ledger.OutputBasic(1_000_000, ledger.SigLockFromED25519PrivateKey(key))
	builder := txbuildercore.New(ledger.L(0).UpgradeIndex())
	builder.ConsumeOutput(output.Bytes(), base.MustNewOutputID(base.RandomTransactionID(false, 0, base.T(0, 0)), 0))
	builder.ProduceOutput(output.Bytes())
	builder.PutSignatureUnlock(0)
	builder.SetTimestamp(base.T(1_400_000_000, 0))
	builder.ComputeInputCommitment()
	builder.SignED25519(key)
	tx, err := transaction.Parse(builder.Bytes())
	require.NoError(t, err)
	require.True(t, ledger.ClockTime(tx.Timestamp()).After(time.Now()), "clock conversion must not overflow into the past")

	for _, test := range []struct {
		name      string
		from      peer.ID
		source    txmetadata.SourceType
		wanted    bool
		penalized []peer.ID
	}{
		{"gossip", "source", txmetadata.SourceTypePeer, false, []peer.ID{"source"}},
		{"pulled", "source", txmetadata.SourceTypePulled, true, []peer.ID{"source"}},
		{"API", "", txmetadata.SourceTypeAPI, false, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := &farFutureTestEnvironment{}
			q := &TxInputQueue{environment: env}
			q.processValidated(tx, &txmetadata.TransactionMetadata{SourceTypeNonPersistent: test.source}, test.from, test.wanted)
			require.False(t, env.persisted, "must be dropped before persisting")
			require.False(t, env.gossiped, "must be dropped before gossiping")
			require.Equal(t, test.penalized, env.penalized)
		})
	}
}
