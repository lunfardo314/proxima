// Tests for the UTXO indexing refactor (ledger/multistate/utxo_indexing.md):
// the index-value tuple at output element index 1 drives trie
// indexing, and the lock at index 2 is arbitrary EasyFL bytecode.
//
// Custom locks here are compiled inline at test time — the goal is to
// exercise the "any EasyFL author can ship a lock" path without
// extending the library. The HTLC primitive in def/timelock.easyfl is
// the one exception: it lives in the library and is exercised through
// its Go wrapper ledger.HTLC.

package tests

import (
	"crypto/ed25519"
	"slices"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// compileLock compiles inline EasyFL source to bytecode using the latest
// library. Used to mint per-test custom lock bytecodes without touching
// the library registration.
func compileLock(t *testing.T, src string) []byte {
	t.Helper()
	_, _, bin, err := ledger.L(base.MaxSlot).CompileExpression(src)
	require.NoError(t, err, "compile %s", src)
	return bin
}

// customLock is a generic ledger.Lock built from inline-compiled EasyFL
// bytecode plus a list of index values. The framework cares only about
// the two output elements (index-value tuple at 1, lock bytecode at 2);
// no library registration is required.
type customLock struct {
	name        string
	bytecode    []byte
	indexValues [][]byte
}

func (c *customLock) Name() string          { return c.name }
func (c *customLock) String() string        { return c.name }
func (c *customLock) IndexValues() [][]byte { return c.indexValues }
func (c *customLock) LockBytecode() []byte  { return c.bytecode }

// fundedSigLock returns a fresh test environment plus a sig-locked
// account funded from the faucet.
func fundedSigLock(t *testing.T, amount uint64) (*utxodb.UTXODB, ed25519.PrivateKey, ledger.SigLock) {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	priv, _, addr := u.GenerateAddress(0)
	require.NoError(t, u.TokensFromFaucet(addr, amount))
	return u, priv, addr
}

// depositToLock builds and submits a tx that takes the source holder's
// sig-locked UTXOs and produces a single output locked by `lock` for
// `amount` tokens (with remainder back to source). Returns the new
// output's ID.
func depositToLock(
	t *testing.T,
	u *utxodb.UTXODB,
	srcKey ed25519.PrivateKey,
	src ledger.SigLock,
	lock ledger.Lock,
	amount uint64,
) base.OutputID {
	t.Helper()
	td, err := u.MakeTransferInputData(srcKey, nil, base.NilLedgerTime)
	require.NoError(t, err)
	td.WithTargetLock(lock).WithAmount(amount)

	outs, err := u.DoTransferOutputs(td)
	require.NoError(t, err)

	// MakeTransferTransaction may emit the remainder before the target,
	// and for sigLock targets both share the same bytecode at slot 2 —
	// so disambiguate by matching BOTH the lock bytecode at slot 2 AND
	// the index-value tuple at slot 1.
	wantIV := ledger.IndexValuesTupleBytes(lock.IndexValues())
	wantLockBin := lock.LockBytecode()
	for _, o := range outs {
		gotLockBin, _ := o.Output.At(int(ledger.ConstraintIndexLock))
		gotIV, _ := o.Output.At(int(ledger.ConstraintIndexIndexValues))
		if slices.Equal(gotLockBin, wantLockBin) && slices.Equal(gotIV, wantIV) {
			return o.ID
		}
	}
	t.Fatalf("depositToLock: produced output with the requested lock not found")
	return base.OutputID{}
}

// spendCustomLockedOutput consumes the given custom-locked output and
// produces a single sig-locked remainder output back to dst, using the
// supplied unlock-parameters bytes at ConstraintIndexLock. The signer
// key signs the transaction (whose holderID matters only for locks that
// check `txHolderID(txSignatureData)`).
func spendCustomLockedOutput(
	t *testing.T,
	u *utxodb.UTXODB,
	signer ed25519.PrivateKey,
	in *ledger.OutputWithID,
	unlockParams []byte,
	dst ledger.SigLock,
	txSlotOverride ...uint32,
) error {
	t.Helper()

	txb := exhelp.New()
	idx, err := txb.ConsumeOutput(in.Output, in.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, unlockParams)

	rem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(in.Output.TokenBalance())).WithLock(dst)
	})
	_, err = txb.ProduceOutput(rem)
	require.NoError(t, err)

	// Default tx timestamp = pace ticks after the input; callers can
	// override the slot to put tx before/after a deadline.
	lib := ledger.L(in.ID.Slot())
	ts := in.ID.Timestamp().AddTicks(int(lib.TransactionPace))
	if len(txSlotOverride) > 0 {
		ts = base.T(txSlotOverride[0], 0)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(signer)

	return u.AddTransaction(txb.Bytes())
}

// utxoExistsAt confirms that calling GetUTXOIDsForController with the
// given index value returns the given output ID at least once.
func utxoExistsAt(t *testing.T, u *utxodb.UTXODB, indexValue []byte, want base.OutputID) bool {
	t.Helper()
	ids, err := u.StateReader().GetUTXOIDsForController(indexValue)
	require.NoError(t, err)
	return slices.Contains(ids, want)
}

// loadOutput fetches a UTXO by ID from current state.
func loadOutput(t *testing.T, u *utxodb.UTXODB, id base.OutputID) *ledger.OutputWithID {
	t.Helper()
	rdr := u.SugaredStateReader()
	o, err := rdr.GetOutputWithID(id)
	require.NoError(t, err)
	return o
}

// anyoneCanSpendBytecode is the trivial lock that accepts every produced
// and every consumed output. Used by the indexing-shape tests where the
// unlock semantics aren't the focus — we still consume the UTXO at the
// end to prove the lock evaluates correctly on the spend path.
func anyoneCanSpendBytecode(t *testing.T) []byte {
	return compileLock(t, `or(selfIsProducedOutput, selfIsConsumedOutput)`)
}

// --------------------------------------------------------------------------
// 1. Empty index-value tuple → output settles, no controller index entry
//    is created, but the UTXO is fully spendable (anyone-can-spend body).
// --------------------------------------------------------------------------

func TestUTXOIndexing_EmptyIndexTuple(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)

	lock := &customLock{
		name:        "anyone",
		bytecode:    anyoneCanSpendBytecode(t),
		indexValues: nil, // no index entries
	}

	id := depositToLock(t, u, srcKey, srcAddr, lock, 100_000_000)

	require.True(t, u.SugaredStateReader().HasUTXO(id), "deposited UTXO must exist")

	out := loadOutput(t, u, id)
	require.Empty(t, out.Output.IndexValues(), "expected empty index-value tuple")

	// Probe a few candidate lookup keys — none should return the UTXO.
	for _, probe := range [][]byte{srcAddr[:], lock.LockBytecode(), {0x00}} {
		require.False(t, utxoExistsAt(t, u, probe, id),
			"non-indexed UTXO must not be reachable via controller scan")
	}

	// And it really is spendable — the "anyone" body must validate.
	require.NoError(t,
		spendCustomLockedOutput(t, u, srcKey, out, nil, srcAddr),
		"empty-index UTXO must still be spendable",
	)
	require.False(t, u.SugaredStateReader().HasUTXO(id), "UTXO must be gone after spend")
}

// --------------------------------------------------------------------------
// 2. Sparse index-value tuple — empty entries silently skipped, UTXO
//    spendable, and the spend correctly removes both non-empty trie
//    entries.
// --------------------------------------------------------------------------

func TestUTXOIndexing_SparseIndexTuple(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)

	v0 := blake2b.Sum256([]byte("alpha"))
	v2 := blake2b.Sum256([]byte("gamma"))
	lock := &customLock{
		name:     "sparse",
		bytecode: anyoneCanSpendBytecode(t),
		indexValues: [][]byte{
			v0[:], // position 0
			nil,   // position 1 — silently skipped
			v2[:], // position 2
		},
	}

	id := depositToLock(t, u, srcKey, srcAddr, lock, 100_000_000)

	require.True(t, utxoExistsAt(t, u, v0[:], id), "indexed under v0")
	require.True(t, utxoExistsAt(t, u, v2[:], id), "indexed under v2")

	// Empty entry is not reachable — try a likely-empty 32-byte value.
	require.False(t, utxoExistsAt(t, u, make([]byte, 32), id),
		"empty entry must not produce a trie record")

	// Spend the UTXO; both trie entries must be cleared.
	out := loadOutput(t, u, id)
	require.NoError(t, spendCustomLockedOutput(t, u, srcKey, out, nil, srcAddr))
	require.False(t, utxoExistsAt(t, u, v0[:], id), "trie entry v0 must be gone after spend")
	require.False(t, utxoExistsAt(t, u, v2[:], id), "trie entry v2 must be gone after spend")
}

// --------------------------------------------------------------------------
// 3. Multi-target lock — three index values, all queryable; spend clears
//    all three trie entries.
// --------------------------------------------------------------------------

func TestUTXOIndexing_MultiTarget(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)

	v := []base.HolderID{
		blake2b.Sum256([]byte("alpha")),
		blake2b.Sum256([]byte("beta")),
		blake2b.Sum256([]byte("gamma")),
	}
	lock := &customLock{
		name:        "multi",
		bytecode:    anyoneCanSpendBytecode(t),
		indexValues: [][]byte{v[0][:], v[1][:], v[2][:]},
	}

	id := depositToLock(t, u, srcKey, srcAddr, lock, 100_000_000)
	for i, val := range v {
		require.True(t, utxoExistsAt(t, u, val[:], id), "indexed under v[%d]", i)
	}

	out := loadOutput(t, u, id)
	require.NoError(t, spendCustomLockedOutput(t, u, srcKey, out, nil, srcAddr))
	for i, val := range v {
		require.False(t, utxoExistsAt(t, u, val[:], id), "trie entry v[%d] must be gone after spend", i)
	}
}

// --------------------------------------------------------------------------
// 4. Pure time-lock — slot threshold lives in the index-value tuple,
//    spending before threshold fails, at/after threshold succeeds.
// --------------------------------------------------------------------------

func TestUTXOIndexing_TimeLock(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)

	src := `
		and(
			require(equal(selfBlockIndex, lockConstraintIndex), !!!locks_must_be_at_lockConstraintIndex),
			or(
				selfIsProducedOutput,
				and(selfIsConsumedOutput, lessOrEqualThan(selfIndexValue(0), txSlot))
			)
		)`
	bytecode := compileLock(t, src)

	srcOuts, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, srcOuts)
	parsed, err := ledger.ParseAndSortOutputData(srcOuts, nil)
	require.NoError(t, err)
	deadlineSlot := parsed[0].ID.Slot() + 5

	threshold := []byte{
		byte(deadlineSlot >> 24),
		byte(deadlineSlot >> 16),
		byte(deadlineSlot >> 8),
		byte(deadlineSlot),
	}
	lock := &customLock{
		name:        "timelock-lock",
		bytecode:    bytecode,
		indexValues: [][]byte{threshold},
	}

	id := depositToLock(t, u, srcKey, srcAddr, lock, 100_000_000)
	out := loadOutput(t, u, id)

	require.True(t, utxoExistsAt(t, u, threshold, id), "indexable by deadline slot")

	require.Error(t,
		spendCustomLockedOutput(t, u, srcKey, out, nil, srcAddr, deadlineSlot-1),
		"spend before deadline must fail",
	)
	require.NoError(t,
		spendCustomLockedOutput(t, u, srcKey, out, nil, srcAddr, deadlineSlot),
		"spend at/after deadline must succeed",
	)
}

// --------------------------------------------------------------------------
// 5. Pure hash-lock — preimage in unlock params, hash in index value.
// --------------------------------------------------------------------------

func TestUTXOIndexing_HashLock(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)

	src := `
		and(
			require(equal(selfBlockIndex, lockConstraintIndex), !!!locks_must_be_at_lockConstraintIndex),
			or(
				selfIsProducedOutput,
				and(
					selfIsConsumedOutput,
					equal(blake2b(selfUnlockParameters), selfIndexValue(0))
				)
			)
		)`
	bytecode := compileLock(t, src)

	preimage := []byte("open sesame")
	hash := blake2b.Sum256(preimage)
	lock := &customLock{
		name:        "hashlock",
		bytecode:    bytecode,
		indexValues: [][]byte{hash[:]},
	}

	id := depositToLock(t, u, srcKey, srcAddr, lock, 100_000_000)
	require.True(t, utxoExistsAt(t, u, hash[:], id))

	out := loadOutput(t, u, id)
	require.Error(t,
		spendCustomLockedOutput(t, u, srcKey, out, []byte("wrong"), srcAddr),
		"wrong preimage must be rejected",
	)
	require.NoError(t,
		spendCustomLockedOutput(t, u, srcKey, out, preimage, srcAddr),
		"correct preimage must succeed",
	)
}

// --------------------------------------------------------------------------
// 6. HTLC (library lock) — variant A (bearer preimage). Both paths
//    exercised, both index values queryable.
// --------------------------------------------------------------------------

func TestUTXOIndexing_HTLC_PreimagePathBeforeDeadline(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)
	_, _, holderAddr := u.GenerateAddress(1)

	preimage := []byte("htlc-secret")
	hash := blake2b.Sum256(preimage)

	// Far-future deadline so we spend on the preimage path.
	srcOuts, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	parsed, err := ledger.ParseAndSortOutputData(srcOuts, nil)
	require.NoError(t, err)
	deadline := parsed[0].ID.Slot() + 1000

	htlc := &ledger.HTLC{
		HolderID: base.HolderID(holderAddr),
		Hash:     hash,
		Deadline: deadline,
	}
	id := depositToLock(t, u, srcKey, srcAddr, htlc, 100_000_000)
	out := loadOutput(t, u, id)

	require.True(t, utxoExistsAt(t, u, holderAddr[:], id), "indexed by holder")
	require.True(t, utxoExistsAt(t, u, hash[:], id), "indexed by hash")

	require.Error(t,
		spendCustomLockedOutput(t, u, srcKey, out, []byte("nope"), srcAddr),
		"wrong preimage must be rejected on preimage path",
	)
	require.NoError(t,
		spendCustomLockedOutput(t, u, srcKey, out, preimage, srcAddr),
		"correct preimage must succeed on preimage path",
	)
}

func TestUTXOIndexing_HTLC_SignaturePathAfterDeadline(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 1_000_000_000)
	holderKey, _, holderAddr := u.GenerateAddress(1)

	preimage := []byte("dont-need-it")
	hash := blake2b.Sum256(preimage)

	srcOuts, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	parsed, err := ledger.ParseAndSortOutputData(srcOuts, nil)
	require.NoError(t, err)
	deadline := parsed[0].ID.Slot() + 3

	htlc := &ledger.HTLC{
		HolderID: base.HolderID(holderAddr),
		Hash:     hash,
		Deadline: deadline,
	}
	id := depositToLock(t, u, srcKey, srcAddr, htlc, 100_000_000)
	out := loadOutput(t, u, id)

	require.Error(t,
		spendCustomLockedOutput(t, u, holderKey, out, nil, holderAddr, deadline-1),
		"sig path before deadline must fail",
	)

	otherKey, _, _ := u.GenerateAddress(99)
	require.Error(t,
		spendCustomLockedOutput(t, u, otherKey, out, nil, holderAddr, deadline),
		"sig path requires the reclaim holder's key",
	)

	require.NoError(t,
		spendCustomLockedOutput(t, u, holderKey, out, nil, holderAddr, deadline),
		"sig path with reclaim holder must succeed at deadline",
	)
}

// --------------------------------------------------------------------------
// 7. Index-value collision: a sigLock(H) and a custom lock that publishes
//    the same 32-byte value H both land under the same trie prefix. The
//    lookup returns both UTXOIDs; the caller distinguishes by parsing the
//    lock bytecode at output element 2. We then spend the custom one to
//    show the trie correctly removes only the matching record.
// --------------------------------------------------------------------------

func TestUTXOIndexing_CollisionWithSigLock(t *testing.T) {
	u, srcKey, srcAddr := fundedSigLock(t, 2_000_000_000)

	_, _, victimAddr := u.GenerateAddress(7)
	shared := victimAddr[:]

	// 7a. A canonical sigLock(victim) UTXO.
	sigID := depositToLock(t, u, srcKey, srcAddr, victimAddr, 100_000_000)

	// 7b. A custom lock that publishes the same 32-byte value but is not
	//     a sigLock — the bytecode at slot 2 differs, but the index entry
	//     under `shared` is identical.
	customID := depositToLock(t, u, srcKey, srcAddr, &customLock{
		name:        "alias",
		bytecode:    anyoneCanSpendBytecode(t),
		indexValues: [][]byte{shared},
	}, 100_000_000)

	ids, err := u.StateReader().GetUTXOIDsForController(shared)
	require.NoError(t, err)
	require.ElementsMatch(t, []base.OutputID{sigID, customID}, ids,
		"both UTXOs must be returned under the same controller key")

	// Distinguish by parsing the bytecode at output element index 2.
	rdr := u.SugaredStateReader()
	classifyAs := func(id base.OutputID) string {
		o, err := rdr.GetOutputWithID(id)
		require.NoError(t, err)
		lockBin, err := o.Output.At(int(ledger.ConstraintIndexLock))
		require.NoError(t, err)
		if slices.Equal(lockBin, ledger.SigLockBytecode()) {
			return ledger.SigLockName
		}
		return "custom"
	}
	require.Equal(t, ledger.SigLockName, classifyAs(sigID))
	require.Equal(t, "custom", classifyAs(customID))

	// Spend the custom UTXO; the sigLock UTXO must remain reachable.
	out := loadOutput(t, u, customID)
	require.NoError(t, spendCustomLockedOutput(t, u, srcKey, out, nil, srcAddr))
	remaining, err := u.StateReader().GetUTXOIDsForController(shared)
	require.NoError(t, err)
	require.Equal(t, []base.OutputID{sigID}, remaining,
		"only the sigLock UTXO must remain after spending the custom one")
}
