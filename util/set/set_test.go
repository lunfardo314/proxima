package set

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestByteSet(t *testing.T) {
	s := NewBytesSet()
	require.EqualValues(t, 0, s.Size())
	for i := 0; i < 256; i++ {
		require.False(t, s.Contains(byte(i)))
	}

	s1 := NewBytesSet(1, 5, 121)
	require.True(t, s1.Contains(1))
	require.True(t, s1.Contains(5))
	require.True(t, s1.Contains(121))
	require.False(t, s1.Contains(20))
	require.EqualValues(t, 3, s1.Size())

	s2 := NewBytesSet(8, 10, 121, 5, 20)
	require.True(t, s2.Contains(8))
	require.True(t, s2.Contains(10))
	require.True(t, s2.Contains(121))
	require.True(t, s2.Contains(5))
	require.True(t, s2.Contains(20))
	require.False(t, s2.Contains(255))
	require.EqualValues(t, 5, s2.Size())

	s3 := UnionByteSet(*s1, *s2)
	require.EqualValues(t, 6, s3.Size())
	str := s3.String()
	t.Logf("s3 = %s", str)
	require.EqualValues(t, str, "[1 5 8 10 20 121]")
}
