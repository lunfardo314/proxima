package vertex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPool(t *testing.T) {
	a0 := GetVertexArray256(0)
	require.EqualValues(t, 0, len(a0))
	DisposeVertexArray256(a0)

	a5 := GetVertexArray256(5)
	require.EqualValues(t, 5, len(a5))
	DisposeVertexArray256(a5)

	a255 := GetVertexArray256(255)
	require.EqualValues(t, 255, len(a255))
	DisposeVertexArray256(a255)

	GetVertexArray256(0)
	GetVertexArray256(5)
	GetVertexArray256(5)
}
