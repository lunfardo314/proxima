package txinput_queue

import (
	"math"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util/ingate"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestMalformedAmountDropped checks the input queue's refusal of a transaction with an output balance
// outside int64, offset by another output so it passes parsing and partial-context validation. Such a
// transaction is dropped before it is persisted, relayed or attached, from every source; a transaction
// with ordinary amounts from the same sender goes through.
func TestMalformedAmountDropped(t *testing.T) {
	key := ledger.InitWithTestingLedgerData(ledger.WithTransactionPace(3), func(p *ledger.InitParameters) {
		p.GenesisTimeUnix -= 60
	})
	t.Cleanup(ledger.ResetForTesting)
	lock := ledger.SigLockFromED25519PrivateKey(key)

	makeTx := func(balance uint64, other int64) *transaction.Transaction {
		builder := txbuildercore.New(ledger.L(0).UpgradeIndex())
		builder.ConsumeOutput(ledger.OutputBasic(1_000_000, lock).Bytes(), base.MustNewOutputID(base.RandomTransactionID(false, 0, base.T(0, 0)), 0))
		builder.ProduceOutput(ledger.OutputBasic(int64(balance), lock).Bytes()) // the encoder writes uint64(amount): the raw value
		builder.ProduceOutput(ledger.OutputBasic(other, lock).Bytes())
		builder.PutSignatureUnlock(0)
		builder.SetTimestamp(base.T(0, 5))
		builder.ComputeInputCommitment()
		builder.SignED25519(key)
		tx, err := transaction.Parse(builder.Bytes())
		require.NoError(t, err)
		return tx
	}
	source, other := peer.ID("source"), peer.ID("other")
	run := func(tx *transaction.Transaction, src txmetadata.SourceType, from peer.ID, wanted bool) *futureTimestampTestEnvironment {
		holder, err := tx.HolderID()
		require.NoError(t, err)
		env := &futureTimestampTestEnvironment{gossipTestEnvironment{self: "self", peers: []peer.ID{source, other}}}
		q := &TxInputQueue{
			environment: env,
			inGate:      ingate.New[base.TransactionID](time.Minute, cleanIfExceeds, inGateMaxSize),
			txSenders:   map[base.HolderID]*seenTimestamps{holder: {}},
			metrics: metrics{
				nonSequencerTxCounter: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_nonsequencer"}),
				gossipedCounter:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_gossiped"}),
			},
		}
		q.processValidated(tx, &txmetadata.TransactionMetadata{SourceTypeNonPersistent: src}, from, wanted)
		return env
	}

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
		env := run(makeTx(math.MaxUint64, 2), src.source, src.from, src.wanted) // the balance reads as -1
		require.Nil(t, env.persisted, src.name)
		require.Empty(t, env.recipients, src.name)
	}

	// ordinary amounts: persisted and relayed
	tx := makeTx(1_000, 2)
	env := run(tx, txmetadata.SourceTypeUndef, source, false)
	require.Same(t, tx, env.persisted)
	require.Equal(t, []peer.ID{other}, env.recipients)
}
