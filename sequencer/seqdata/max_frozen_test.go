package seqdata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The bound on frozen coverage distinguishes absent (unlimited) from 0 (freeze
// nothing). Both must survive a round trip through the UTXO bytes unchanged.
func TestMaxFrozenRoundTrip(t *testing.T) {
	absent := New().SetName("a")
	require.NotContains(t, string(absent.Bytes()), "max_frozen", "absent bound must not be serialised")
	back, err := FromBytes(absent.Bytes())
	require.NoError(t, err)
	_, limited := back.MaxFrozenCoverage()
	require.False(t, limited)
	require.False(t, back.FreezesNothing())

	zero := New().SetName("a").SetMaxFrozenCoverage(0)
	require.Contains(t, string(zero.Bytes()), `"max_frozen":0`, "a bound of 0 must be serialised, it is not the same as absent")
	back, err = FromBytes(zero.Bytes())
	require.NoError(t, err)
	bound, limited := back.MaxFrozenCoverage()
	require.True(t, limited)
	require.EqualValues(t, 0, bound)
	require.True(t, back.FreezesNothing())
	require.Equal(t, zero.Bytes(), back.Bytes())

	some := New().SetMaxFrozenCoverage(1_000_000)
	back, err = FromBytes(some.Bytes())
	require.NoError(t, err)
	bound, limited = back.MaxFrozenCoverage()
	require.True(t, limited)
	require.EqualValues(t, 1_000_000, bound)
	require.False(t, back.FreezesNothing())

	require.NotContains(t, string(back.Clone().RemoveMaxFrozenCoverage().Bytes()), "max_frozen")
}

// Clone must copy the bound, not the pointer, or changing a clone's bound
// would change the original.
func TestCloneDoesNotShareMaxFrozen(t *testing.T) {
	sd := New().SetMaxFrozenCoverage(5)
	clone := sd.Clone()
	*clone.MaxFrozen = 7
	bound, _ := sd.MaxFrozenCoverage()
	require.EqualValues(t, 5, bound)
}

// The per-epoch count cap: absent means the default, 0 is kept as 0 and means
// nothing is frozen, and Clone copies the value.
func TestMaxFrozenDelegations(t *testing.T) {
	sd := New().SetName("a")
	require.EqualValues(t, DefaultMaxFrozenDelegations, sd.MaxFrozenDelegationsPerEpoch())
	require.NotContains(t, string(sd.Bytes()), "max_frozen_delegations")
	require.False(t, sd.FreezesNothing())

	zero := New().SetMaxFrozenDelegationsPerEpoch(0)
	require.Contains(t, string(zero.Bytes()), `"max_frozen_delegations":0`)
	back, err := FromBytes(zero.Bytes())
	require.NoError(t, err)
	require.EqualValues(t, 0, back.MaxFrozenDelegationsPerEpoch())
	require.True(t, back.FreezesNothing())

	clone := back.Clone()
	*clone.MaxFrozenDelegations = 50
	require.EqualValues(t, 0, back.MaxFrozenDelegationsPerEpoch())
	require.EqualValues(t, DefaultMaxFrozenDelegations, clone.RemoveMaxFrozenDelegationsPerEpoch().MaxFrozenDelegationsPerEpoch())
}
