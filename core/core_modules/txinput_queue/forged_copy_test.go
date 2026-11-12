package txinput_queue

import (
	"bytes"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// forgedCopyTestEnvironment records what the input queue does with each copy of a
// transaction: which bytes it persists and gossips, and which peers it reports.
type forgedCopyTestEnvironment struct {
	environment
	persisted [][]byte
	gossiped  int
	invalidTx []peer.ID
}

func (*forgedCopyTestEnvironment) SelfPeerID() peer.ID                             { return "self" }
func (*forgedCopyTestEnvironment) LogTx(time.Time, string, ...base.TransactionID)  {}
func (*forgedCopyTestEnvironment) WarnTopicf(string, int, string, ...any)          {}
func (*forgedCopyTestEnvironment) IncCounter(string)                               {}
func (*forgedCopyTestEnvironment) DecCounter(string)                               {}
func (*forgedCopyTestEnvironment) EvidenceValidTxFromPeer(peer.ID)                 {}
func (*forgedCopyTestEnvironment) EvidenceNumberOfTxDependencies(int)              {}
func (*forgedCopyTestEnvironment) GetLatestReliableBranch() *multistate.BranchData { return nil }

// Unsolicited copies stop at the attach gate by load shedding, pulled ones are not
// attached: the test is about what happens before attachment.
func (*forgedCopyTestEnvironment) IsSnapshotting() bool                            { return true }
func (*forgedCopyTestEnvironment) ClockCatchUpWithLedgerTime(base.LedgerTime) bool { return false }
func (*forgedCopyTestEnvironment) AttachFun() func(*transaction.Transaction, ...attacher.AttachTxOption) {
	return func(*transaction.Transaction, ...attacher.AttachTxOption) {}
}

func (e *forgedCopyTestEnvironment) EvidenceInvalidTxFromPeer(id peer.ID) {
	e.invalidTx = append(e.invalidTx, id)
}

func (e *forgedCopyTestEnvironment) MustPersistTxBytes(tx *transaction.Transaction) {
	e.persisted = append(e.persisted, tx.Bytes())
}

func (e *forgedCopyTestEnvironment) GossipTxBytesToPeers([]byte, base.TransactionID, ...peer.ID) {
	e.gossiped++
}

// makeGenuineAndForgedCopy returns a signed transaction and a copy of it with one
// signature byte flipped. The transaction ID does not cover the signature data, so
// both share one ID, and the copy keeps the genuine signer's public key. The ledger folds
// upgrades 1 to 5 only: from upgrade 6 the ID commits to the signer and to every element's
// length, so the copies these tests need exist only under the earlier transaction ID.
func makeGenuineAndForgedCopy(t *testing.T) (genuine, forged []byte, txid base.TransactionID) {
	privKey := ledger.InitWithTestingLedgerData(ledger.WithFoldedUpgrades(5))
	t.Cleanup(ledger.ResetForTesting)
	u := utxodb.NewUTXODB(privKey, true)
	_, _, addr := u.GenerateAddress(1)
	genuine, err := u.MakeTransactionFromFaucet(addr)
	require.NoError(t, err)

	tx, err := transaction.Parse(genuine)
	require.NoError(t, err)
	sig, err := tx.BytesAtPath(ledger.PathToSignature)
	require.NoError(t, err)
	pos := bytes.Index(genuine, sig)
	require.True(t, pos >= 0)

	forged = bytes.Clone(genuine)
	forged[pos+1] ^= 0xff // first byte of the signature proper, after the signature type
	forgedTx, err := transaction.Parse(forged)
	require.NoError(t, err)
	require.Equal(t, tx.ID(), forgedTx.ID(), "the copy must share the genuine transaction ID")
	return genuine, forged, tx.ID()
}

func newForgedCopyTestQueue(env *forgedCopyTestEnvironment) *TxInputQueue {
	q := &TxInputQueue{
		environment: env,
		inGate:      newInGate[base.TransactionID](time.Minute, cleanIfExceeds, inGateMaxSize),
		txSenders:   make(map[base.HolderID]*seenTimestamps),
		checkNonSeq: true,
	}
	q.filterHitCounter = prometheus.NewCounter(prometheus.CounterOpts{Name: "filter_hit"})
	q.nonSequencerTxCounter = prometheus.NewCounter(prometheus.CounterOpts{Name: "non_seq"})
	q.gossipedCounter = prometheus.NewCounter(prometheus.CounterOpts{Name: "gossiped"})
	return q
}

func peerInput(txBytes []byte, txid base.TransactionID, from peer.ID) *Input {
	return &Input{
		Cmd:        CmdFromPeer,
		PrefixTxID: txid,
		TxBytes:    txBytes,
		TxMetaData: &txmetadata.TransactionMetadata{SourceTypeNonPersistent: txmetadata.SourceTypePeer},
		FromPeer:   from,
	}
}

// TestForgedCopyDoesNotBlockGenuine covers a copy of a transaction with a spoiled
// signature reaching the node before the genuine one. The copy must be dropped and
// its sender reported, without leaving the ID seen in the in-gate or its timestamp in
// the sender pace gate: either would make the node refuse the genuine transaction.
func TestForgedCopyDoesNotBlockGenuine(t *testing.T) {
	genuine, forged, txid := makeGenuineAndForgedCopy(t)

	t.Run("unsolicited", func(t *testing.T) {
		env := &forgedCopyTestEnvironment{}
		q := newForgedCopyTestQueue(env)

		q.fromPeer(peerInput(forged, txid, "attacker"))
		require.Empty(t, env.persisted, "a forged copy must not be persisted")
		require.Zero(t, env.gossiped, "a forged copy must not be gossiped")
		require.Equal(t, []peer.ID{"attacker"}, env.invalidTx, "the sender of a forged copy must be reported")

		q.fromPeer(peerInput(genuine, txid, "honest"))
		require.Equal(t, [][]byte{genuine}, env.persisted, "the genuine transaction must pass after a forged copy")
		require.Equal(t, 1, env.gossiped)

		// once a copy is accepted, later ones are repeats, forged or not
		q.fromPeer(peerInput(forged, txid, "attacker"))
		q.fromPeer(peerInput(genuine, txid, "honest"))
		require.Len(t, env.persisted, 1)
		require.Len(t, env.invalidTx, 1, "a repeat is filtered before the signature check")
	})

	t.Run("pulled", func(t *testing.T) {
		env := &forgedCopyTestEnvironment{}
		q := newForgedCopyTestQueue(env)
		q.AddPulledTransaction(txid)

		// a forged reply to the pull must not use up the pass of the genuine reply
		q.fromPeer(peerInput(forged, txid, "attacker"))
		require.Empty(t, env.persisted)
		require.True(t, q.inGate.wasPulled(txid), "a forged reply must leave the ID pulled")

		q.fromPeer(peerInput(genuine, txid, "honest"))
		require.Equal(t, [][]byte{genuine}, env.persisted, "the genuine reply must pass after a forged one")
		require.Zero(t, env.gossiped, "a pulled transaction is not gossiped")
		require.False(t, q.inGate.wasPulled(txid), "an accepted reply clears the pull")

		q.fromPeer(peerInput(genuine, txid, "honest"))
		require.Len(t, env.persisted, 1, "a second reply to one pull is a repeat")
	})
}
