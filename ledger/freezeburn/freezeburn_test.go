package freezeburn

import (
	"crypto/ed25519"
	"encoding/binary"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/blocklist"
	"github.com/stretchr/testify/require"
)

func init() {
	// the testing ledger folds every listed upgrade, upgrade 8 included, into its genesis
	ledger.InitWithTestingLedgerData()
}

// TestListMatchesLibrary checks that the list the node deletes is the one the library commits
// to: its total equals constBurnedAmount and its hash constBurnedListHash.
func TestListMatchesLibrary(t *testing.T) {
	require.Len(t, BurnedOutputIDs(), 782)
	require.EqualValues(t, 5_005_843_389_176, BurnedAmount())

	lib := ledger.L(0)
	require.True(t, lib.HasUpgrade8())
	amount, err := lib.EvalFromSource(nil, "constBurnedAmount")
	require.NoError(t, err)
	require.Len(t, amount, 8)
	require.Equal(t, BurnedAmount(), binary.BigEndian.Uint64(amount))

	hash, err := lib.EvalFromSource(nil, "constBurnedListHash")
	require.NoError(t, err)
	h := ListHash()
	require.Equal(t, h[:], hash)
}

// TestNoCrossingWhenFolded checks that on a ledger whose genesis folds upgrade 8 no branch
// crosses it, so nothing is deleted or subtracted.
func TestNoCrossingWhenFolded(t *testing.T) {
	require.False(t, IsCrossing(0, 1))
	require.False(t, IsCrossing(ledger.Upgrade8Slot-1, ledger.Upgrade8Slot))
	require.Zero(t, BurnedAt(ledger.Upgrade8Slot-1, ledger.Upgrade8Slot))
	_, listed := BurnSlot()
	require.False(t, listed, "a folded upgrade 8 has no burn slot")
}

// TestCheckInputs checks that every burned output is refused as a policy refusal and that
// another output passes.
func TestCheckInputs(t *testing.T) {
	var other base.OutputID
	require.NoError(t, CheckInputs([]base.OutputID{other}))
	for _, oid := range BurnedOutputIDs() {
		require.ErrorIs(t, CheckInputs([]base.OutputID{other, oid}), blocklist.ErrFrozen)
	}
}

// recordingMutations records what DeleteBurned writes.
type recordingMutations struct {
	outputs []base.OutputID
	chains  []base.ChainID
}

func (m *recordingMutations) InsertDelOutputMutation(oid base.OutputID) {
	m.outputs = append(m.outputs, oid)
}

func (m *recordingMutations) InsertDelChainMutation(id base.ChainID) {
	m.chains = append(m.chains, id)
}

// TestDeleteBurned checks the deletion on a baseline: every present burned output is deleted
// and counted by its own amount, the chain record of a chained one is deleted with it, and an
// absent output is reported missing.
func TestDeleteBurned(t *testing.T) {
	var a, b base.OutputID
	a[0], b[0] = 1, 2
	lock := ledger.SigLockFromED25519PrivateKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	plain := ledger.NewOutput(func(o *ledger.OutputBuilder) { o.WithAmounts(10).WithLock(lock) })
	chained := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(32).WithLock(lock)
		o.MustPushConstraint(ledger.NewChainOrigin(0).Bytes())
	})
	state := map[base.OutputID][]byte{a: plain.Bytes(), b: chained.Bytes()}
	get := func(oid base.OutputID) ([]byte, bool) {
		data, ok := state[oid]
		return data, ok
	}
	t.Cleanup(SetBurnedForTesting(map[base.OutputID]uint64{a: 10, b: 32}))
	require.EqualValues(t, 42, BurnedAmount())

	_, missing := MissingFromBaseline(func(oid base.OutputID) bool { _, ok := state[oid]; return ok })
	require.False(t, missing)
	muts := &recordingMutations{}
	deleted, err := DeleteBurned(muts, get)
	require.NoError(t, err)
	require.EqualValues(t, 42, deleted)
	require.ElementsMatch(t, []base.OutputID{a, b}, muts.outputs)
	require.Equal(t, []base.ChainID{base.MakeOriginChainID(b)}, muts.chains)

	delete(state, b)
	oid, missing := MissingFromBaseline(func(oid base.OutputID) bool { _, ok := state[oid]; return ok })
	require.True(t, missing)
	require.Equal(t, b, oid)
}
