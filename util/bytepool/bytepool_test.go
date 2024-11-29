package bytepool

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBytePool(t *testing.T) {
	s := bytePool[4].Get()
	require.Nil(t, s)
	bytePool[4].Put([]byte("abc"))
	s = bytePool[4].Get()
	require.NotNil(t, s)
	s = bytePool[4].Get()
	require.Nil(t, s)

	a := GetArray(100)
	require.EqualValues(t, 100, len(a))
	ca := cap(a)
	DisposeArray(a)
	a = GetArray(73)
	require.EqualValues(t, 73, len(a))
	require.EqualValues(t, ca, cap(a))

	a = GetArray(1000)
	require.EqualValues(t, 1000, len(a))
	ca = cap(a)
	DisposeArray(a)
	a = GetArray(200)
	require.EqualValues(t, 200, len(a))
	require.EqualValues(t, ca, cap(a))
}
