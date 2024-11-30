package bitset

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBitSet(t *testing.T) {
	var bs Bitset

	require.False(t, bs.Contains(5))
	require.False(t, bs.Contains(0))
	require.False(t, bs.Contains(17))
	require.False(t, bs.Contains(255))

	b := bs.Bytes()
	require.EqualValues(t, 1, len(b))
	bBack, err := BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

	bs.Insert(5)
	require.True(t, bs.Contains(5))
	require.False(t, bs.Contains(0))
	require.False(t, bs.Contains(17))
	require.False(t, bs.Contains(255))
	require.EqualValues(t, 1, len(b))

	b = bs.Bytes()
	bBack, err = BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

	bs.Insert(0)
	require.True(t, bs.Contains(5))
	require.True(t, bs.Contains(0))
	require.False(t, bs.Contains(17))
	require.False(t, bs.Contains(255))
	require.EqualValues(t, 1, len(b))

	b = bs.Bytes()
	require.EqualValues(t, 1, len(b))
	bBack, err = BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

	bs.Remove(5)
	require.False(t, bs.Contains(5))
	require.True(t, bs.Contains(0))
	require.False(t, bs.Contains(17))
	require.False(t, bs.Contains(255))

	b = bs.Bytes()
	require.EqualValues(t, 1, len(b))
	bBack, err = BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

	bs.Insert(17)
	require.False(t, bs.Contains(5))
	require.True(t, bs.Contains(0))
	require.True(t, bs.Contains(17))
	require.False(t, bs.Contains(255))

	b = bs.Bytes()
	require.EqualValues(t, 3, len(b))
	bBack, err = BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

	bs.Remove(20)
	require.False(t, bs.Contains(5))
	require.True(t, bs.Contains(0))
	require.True(t, bs.Contains(17))
	require.False(t, bs.Contains(255))

	bs.Insert(255)
	require.False(t, bs.Contains(5))
	require.True(t, bs.Contains(0))
	require.True(t, bs.Contains(17))
	require.True(t, bs.Contains(255))

	b = bs.Bytes()
	require.EqualValues(t, 32, len(b))
	bBack, err = BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

	bs.Remove(255)
	require.False(t, bs.Contains(5))
	require.True(t, bs.Contains(0))
	require.True(t, bs.Contains(17))
	require.False(t, bs.Contains(255))

	b = bs.Bytes()
	require.EqualValues(t, 3, len(b))
	bBack, err = BitsetFromBytes(b)
	require.NoError(t, err)
	require.EqualValues(t, bs, bBack)

}
