package set

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestByteSet(t *testing.T) {
	s := NewByteSet()
	require.EqualValues(t, 0, s.Size())
	for i := 0; i < 256; i++ {
		require.False(t, s.Contains(byte(i)))
	}

	s1 := NewByteSet(1, 5, 121, 63, 255)
	t.Logf("\n%s", s1.stringMask())
	t.Logf("s1 = %s", s1.String())
	require.True(t, s1.Contains(1))
	require.True(t, s1.Contains(5))
	require.True(t, s1.Contains(121))
	require.False(t, s1.Contains(20))
	require.True(t, s1.Contains(255))
	require.EqualValues(t, 5, s1.Size())

	s2 := NewByteSet(8, 10, 121, 5, 20)
	t.Logf("s2 = %s", s2.String())

	require.True(t, s2.Contains(8))
	require.True(t, s2.Contains(10))
	require.True(t, s2.Contains(121))
	require.True(t, s2.Contains(5))
	require.True(t, s2.Contains(20))
	require.False(t, s2.Contains(255))
	require.EqualValues(t, 5, s2.Size())

	s3 := UnionByteSet(s1, s2)
	t.Logf("union(s1, s2) = s3 = %s", s3.String())
	t.Logf("s3 mask = \n%s", s3.stringMask())

	require.EqualValues(t, 8, s3.Size())
	str := s3.String()
	t.Logf("s3 = %s", str)
	require.EqualValues(t, "[1 5 8 10 20 63 121 255]", str)

	s3.Insert(150)
	require.EqualValues(t, 9, s3.Size())

	s3.Remove(20, 121)
	require.EqualValues(t, 7, s3.Size())

	str = s3.String()
	t.Logf("s3 = %s", str)
	require.EqualValues(t, "[1 5 8 10 63 150 255]", str)

	s3.Remove(0)
	require.EqualValues(t, 7, s3.Size())

	s3.Insert(150)
	require.EqualValues(t, 7, s3.Size())

	s3.Insert(0)
	require.EqualValues(t, 8, s3.Size())

	str = s3.String()
	t.Logf("s3 = %s", str)
	require.EqualValues(t, "[0 1 5 8 10 63 150 255]", str)
}
