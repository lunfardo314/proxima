package txinput_queue

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// withElements rebuilds the wire bytes of a transaction with some of its top-level
// elements replaced, the way an attacker re-encodes a copy.
func withElements(t *testing.T, txBytes []byte, replace map[byte][]byte) []byte {
	tu, err := tuples.TupleFromBytes(txBytes)
	require.NoError(t, err)
	elems := make([][]byte, tu.NumElements())
	for i := range elems {
		elems[i], err = tu.At(i)
		require.NoError(t, err)
	}
	for i, b := range replace {
		elems[i] = b
	}
	return tuples.MakeTupleFromDataElements(elems...).Bytes()
}

// TestCanonicalEncoding checks the encoding rule on its own: what builders write
// passes, and other encodings, some keeping the ID, are refused.
func TestCanonicalEncoding(t *testing.T) {
	genuine, _, txid := makeGenuineAndForgedCopy(t)
	tx, err := transaction.Parse(genuine)
	require.NoError(t, err)
	require.NoError(t, checkCanonicalEncoding(tx), "a transaction made by the builder is canonical")

	endorsements, err := tx.BytesAtPath(ledger.PathToEndorsements)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0}, endorsements, "empty endorsements are written as the 2-byte header")
	sig, err := tx.BytesAtPath(ledger.PathToSignature)
	require.NoError(t, err)

	cases := []struct {
		name    string
		replace map[byte][]byte
		sameID  bool // the copy keeps the genuine ID
	}{
		// the empty endorsements header moved into the transaction constraints
		{"empty tuples swapped", map[byte][]byte{ledger.TxEndorsements: nil, ledger.TxConstraints: {0, 0}}, true},
		// junk after a header that announces only empty elements
		{"bytes after an empty header", map[byte][]byte{ledger.TxEndorsements: {0, 0, 1, 2, 3}}, false},
		// signature data is outside the ID, so padding keeps the ID
		{"padded signature data", map[byte][]byte{ledger.TxSignatureData: append(append([]byte{}, sig...), 0xee)}, true},
		{"short input commitment", map[byte][]byte{ledger.TxInputCommitment: make([]byte, 31)}, false},
		{"explicit baseline of a wrong length", map[byte][]byte{ledger.TxExplicitBaseline: {1, 2, 3}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			copyTx, err := transaction.Parse(withElements(t, genuine, c.replace))
			if err != nil {
				return // refused at parse already: never reaches the queue
			}
			if c.sameID {
				require.Equal(t, txid, copyTx.ID(), "the copy must keep the genuine ID for the case to matter")
			}
			require.Error(t, checkCanonicalEncoding(copyTx))
		})
	}
}

// TestNonCanonicalCopyDoesNotBlockGenuine covers a copy with the genuine ID and a
// non-canonical encoding reaching the node first. It must be neither persisted nor
// gossiped, its sender not penalised, and the genuine transaction must still pass after it.
func TestNonCanonicalCopyDoesNotBlockGenuine(t *testing.T) {
	genuine, _, txid := makeGenuineAndForgedCopy(t)
	tx, err := transaction.Parse(genuine)
	require.NoError(t, err)
	sig, err := tx.BytesAtPath(ledger.PathToSignature)
	require.NoError(t, err)

	copies := map[string][]byte{
		"empty tuples swapped": withElements(t, genuine, map[byte][]byte{ledger.TxEndorsements: nil, ledger.TxConstraints: {0, 0}}),
		"padded signature":     withElements(t, genuine, map[byte][]byte{ledger.TxSignatureData: append(append([]byte{}, sig...), 0xee)}),
	}
	for name, copyBytes := range copies {
		t.Run(name, func(t *testing.T) {
			copyTx, err := transaction.Parse(copyBytes)
			require.NoError(t, err)
			require.Equal(t, txid, copyTx.ID())

			env := &forgedCopyTestEnvironment{}
			q := newForgedCopyTestQueue(env)
			q.fromPeer(peerInput(copyBytes, txid, "attacker"))
			require.Empty(t, env.persisted, "a non-canonical copy must not be persisted")
			require.Zero(t, env.gossiped, "a non-canonical copy must not be gossiped")
			// a node of an older release relays such a copy in good faith
			require.Empty(t, env.invalidTx, "the sender of a non-canonical copy is not penalised")

			q.fromPeer(peerInput(genuine, txid, "honest"))
			require.Equal(t, [][]byte{genuine}, env.persisted, "the genuine transaction must pass after the copy")
		})
	}
}

// TestStrippedTxConstraints covers an honest transaction carrying a transaction-level
// constraint and another encoding of it with the same ID: the genuine one passes, the
// other is refused. Under the transaction ID before upgrade 6, the only one such a copy keeps.
func TestStrippedTxConstraints(t *testing.T) {
	privKey := ledger.InitWithTestingLedgerData(ledger.WithFoldedUpgrades(5))
	t.Cleanup(ledger.ResetForTesting)
	u := utxodb.NewUTXODB(privKey, true)
	priv, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, 1_000_000_000))

	outsData, err := u.StateReader().GetUTXOsForController(addr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(_ *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil
	})
	require.NoError(t, err)
	txb := exhelp.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs[0])
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(addr)
	}))
	require.NoError(t, err)
	txb.PushTxConstraint(easyfl.InlineDataBytecode([]byte{1, 2, 3}))
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	genuine := txb.Bytes()
	require.NoError(t, u.AddTransaction(genuine), "the genuine transaction is valid")

	tx, err := transaction.Parse(genuine)
	require.NoError(t, err)
	require.NoError(t, checkCanonicalEncoding(tx), "an honest transaction with constraints is canonical")

	constraints, err := tx.BytesAtPath(ledger.PathToTxConstraints)
	require.NoError(t, err)
	stripped := withElements(t, genuine, map[byte][]byte{
		ledger.TxEndorsements: append([]byte{0, 0}, constraints...),
		ledger.TxConstraints:  nil,
	})
	strippedTx, err := transaction.Parse(stripped)
	require.NoError(t, err)
	require.Equal(t, tx.ID(), strippedTx.ID(), "the stripped copy keeps the ID")
	require.Zero(t, strippedTx.NumEndorsements(), "the moved bytes are not read as endorsements")
	require.Error(t, checkCanonicalEncoding(strippedTx))
}

// TestPulledNonCanonicalCopyIsTaken covers a transaction some past cone needs whose only
// copy at hand is in another encoding. A pulled transaction is not refused for its
// encoding: nodes of other releases accept it, and refusing it would leave this node
// unable to follow a milestone that includes it.
func TestPulledNonCanonicalCopyIsTaken(t *testing.T) {
	genuine, _, txid := makeGenuineAndForgedCopy(t)
	tx, err := transaction.Parse(genuine)
	require.NoError(t, err)
	sig, err := tx.BytesAtPath(ledger.PathToSignature)
	require.NoError(t, err)
	padded := withElements(t, genuine, map[byte][]byte{ledger.TxSignatureData: append(append([]byte{}, sig...), 0xee)})

	env := &forgedCopyTestEnvironment{}
	q := newForgedCopyTestQueue(env)
	q.AddPulledTransaction(txid)
	q.fromPeer(peerInput(padded, txid, "peer"))
	require.Equal(t, [][]byte{padded}, env.persisted, "a pulled copy is taken whatever its encoding")
	require.Zero(t, env.gossiped, "a pulled transaction is not gossiped")
	require.Empty(t, env.invalidTx)
}
