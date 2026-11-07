package txinput_queue

import (
	"slices"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type gossipTestEnvironment struct {
	environment
	self       peer.ID
	peers      []peer.ID
	recipients []peer.ID
	persisted  *transaction.Transaction
}

func (e *gossipTestEnvironment) SelfPeerID() peer.ID { return e.self }
func (e *gossipTestEnvironment) MustPersistTxBytes(tx *transaction.Transaction) {
	e.persisted = tx
}
func (e *gossipTestEnvironment) GossipTxBytesToPeers(_ []byte, _ base.TransactionID, except ...peer.ID) {
	for _, target := range e.peers {
		if !slices.Contains(except, target) {
			e.recipients = append(e.recipients, target)
		}
	}
}
func (*gossipTestEnvironment) EvidenceNumberOfTxDependencies(int) {}
func (*gossipTestEnvironment) EvidenceValidTxFromPeer(peer.ID)    {}
func (*gossipTestEnvironment) GetOwnSequencerID() *base.ChainID   { return nil }
func (*gossipTestEnvironment) IsSnapshotting() bool               { return false }
func (*gossipTestEnvironment) IncCounter(string)                  {}

func TestValidatedTransactionExcludesGossipSource(t *testing.T) {
	key := ledger.InitWithTestingLedgerData(ledger.WithTransactionPace(3), func(p *ledger.InitParameters) {
		p.GenesisTimeUnix -= 60
	})
	t.Cleanup(ledger.ResetForTesting)
	output := ledger.OutputBasic(1_000_000, ledger.SigLockFromED25519PrivateKey(key))
	builder := txbuildercore.New(0)
	builder.ConsumeOutput(output.Bytes(), base.MustNewOutputID(base.RandomTransactionID(false, 0, base.T(0, 0)), 0))
	builder.ProduceOutput(output.Bytes())
	builder.PutSignatureUnlock(0)
	builder.SetTimestamp(base.T(0, 5))
	builder.ComputeInputCommitment()
	builder.SignED25519(key)
	tx, err := transaction.Parse(builder.Bytes())
	require.NoError(t, err)
	holder, err := tx.HolderID()
	require.NoError(t, err)

	self, source, other := peer.ID("self"), peer.ID("source"), peer.ID("other")
	for _, test := range []struct {
		name string
		from peer.ID
		want []peer.ID
	}{
		{"peer", source, []peer.ID{other}},
		{"API", "", []peer.ID{source, other}},
		{"self", self, []peer.ID{source, other}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := transaction.Parse(builder.Bytes())
			require.NoError(t, err)
			env := &gossipTestEnvironment{self: self, peers: []peer.ID{source, other}}
			q := &TxInputQueue{
				environment: env,
				inGate:      newInGate[base.TransactionID](time.Minute, cleanIfExceeds, inGateMaxSize),
				txSenders:   map[base.HolderID]*seenTimestamps{holder: {}},
				metrics: metrics{
					nonSequencerTxCounter: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_nonsequencer"}),
					gossipedCounter:       prometheus.NewCounter(prometheus.CounterOpts{Name: "test_gossiped"}),
				},
			}
			meta := &txmetadata.TransactionMetadata{SourceTypeNonPersistent: txmetadata.SourceTypePeer}
			if test.from == "" {
				meta.SourceTypeNonPersistent = txmetadata.SourceTypeAPI
			}
			q.processValidated(tx, meta, test.from, false)
			require.Equal(t, test.want, env.recipients)
			require.Same(t, tx, env.persisted)
		})
	}
}
