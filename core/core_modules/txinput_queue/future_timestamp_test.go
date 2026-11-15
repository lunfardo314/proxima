package txinput_queue

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type futureTimestampTestEnvironment struct {
	gossipTestEnvironment
}

func (*futureTimestampTestEnvironment) LogTx(time.Time, string, ...base.TransactionID) {}
func (*futureTimestampTestEnvironment) WarnTopicf(string, int, string, ...any)         {}

// TestFutureTimestampDropped checks the time bound of the input queue, which is the same for every
// source. A transaction stamped more than global.MaxSlotsInTheFuture slots ahead of the ledger time
// is dropped before it is persisted, relayed or attached, whether it arrives as gossip (which carries
// no source type), as a pulled response, or from the API, and whether or not its clock time fits in
// int64 nanoseconds. It is also forgotten by the in-gate, so a later copy is not taken as a repeat.
// A transaction exactly at the bound still passes.
func TestFutureTimestampDropped(t *testing.T) {
	key := ledger.InitWithTestingLedgerData(ledger.WithTransactionPace(3), func(p *ledger.InitParameters) {
		p.GenesisTimeUnix -= 60
	})
	t.Cleanup(ledger.ResetForTesting)

	makeTx := func(ts base.LedgerTime) *transaction.Transaction {
		output := ledger.OutputBasic(1_000_000, ledger.SigLockFromED25519PrivateKey(key))
		builder := txbuildercore.New(ledger.L(0).UpgradeIndex())
		builder.ConsumeOutput(output.Bytes(), base.MustNewOutputID(base.RandomTransactionID(false, 0, base.T(0, 0)), 0))
		builder.ProduceOutput(output.Bytes())
		builder.PutSignatureUnlock(0)
		builder.SetTimestamp(ts)
		builder.ComputeInputCommitment()
		builder.SignED25519(key)
		tx, err := transaction.Parse(builder.Bytes())
		require.NoError(t, err)
		return tx
	}
	source, other := peer.ID("source"), peer.ID("other")
	newQueue := func(tx *transaction.Transaction) (*TxInputQueue, *futureTimestampTestEnvironment) {
		holder, err := tx.HolderID()
		require.NoError(t, err)
		env := &futureTimestampTestEnvironment{gossipTestEnvironment{self: "self", peers: []peer.ID{source, other}}}
		return &TxInputQueue{
			environment: env,
			inGate:      newInGate[base.TransactionID](time.Minute, cleanIfExceeds, inGateMaxSize),
			txSenders:   map[base.HolderID]*seenTimestamps{holder: {}},
			metrics: metrics{
				nonSequencerTxCounter: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_nonsequencer"}),
				gossipedCounter:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_gossiped"}),
			},
		}, env
	}

	nowSlot := ledger.TimeNow().Slot
	far := map[string]base.LedgerTime{
		// beyond the bound, its clock time still within int64 nanoseconds
		"one slot past the bound": base.T(nowSlot+global.MaxSlotsInTheFuture+1, 0),
		// beyond the int64 nanosecond range, where the clock time used to wrap into the past
		"past the clock range": base.T(1_400_000_000, 0),
		"largest slot":         base.T(base.MaxSlot, 0),
	}
	for name, ts := range far {
		t.Run(name, func(t *testing.T) {
			for _, src := range []struct {
				name   string
				source txmetadata.SourceType
				from   peer.ID
				wanted bool
			}{
				{"gossip", txmetadata.SourceTypeUndef, source, false},
				{"pulled", txmetadata.SourceTypePulled, source, true},
				{"API", txmetadata.SourceTypeAPI, "", false},
			} {
				tx := makeTx(ts)
				q, env := newQueue(tx)
				q.inGate.checkPass(tx.ID())
				q.processValidated(tx, &txmetadata.TransactionMetadata{SourceTypeNonPersistent: src.source}, src.from, src.wanted)
				require.Nil(t, env.persisted, src.name)
				require.Empty(t, env.recipients, src.name)
				pass, _ := q.inGate.checkPass(tx.ID())
				require.True(t, pass, "%s: a dropped transaction must be forgotten by the in-gate", src.name)
			}

			// Received from a peer, it is dropped on the txid prefix, before parsing and dedup:
			// the bytes are not even read.
			tx := makeTx(ts)
			q, env := newQueue(tx)
			q.fromPeer(&Input{PrefixTxID: tx.ID(), TxBytes: []byte("not a transaction"), FromPeer: source})
			require.Nil(t, env.persisted)
			require.Empty(t, env.recipients)
			pass, _ := q.inGate.checkPass(tx.ID())
			require.True(t, pass, "the prefix check must leave no in-gate entry")
		})
	}

	// at the bound: taken in, persisted and relayed
	tx := makeTx(base.T(nowSlot+global.MaxSlotsInTheFuture, 0))
	q, env := newQueue(tx)
	q.processValidated(tx, &txmetadata.TransactionMetadata{}, source, false)
	require.Same(t, tx, env.persisted)
	require.Equal(t, []peer.ID{other}, env.recipients)
}
