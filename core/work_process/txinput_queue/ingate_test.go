package txinput_queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInputGate(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		g := newInGate[int](5*time.Second, 10*time.Second, func() time.Duration { return 0 })
		pass, wanted := g.checkPass(1)
		require.True(t, pass)
		require.False(t, wanted)

		pass, _ = g.checkPass(1)
		require.False(t, pass)

		g.addWanted(5)
		pass, wanted = g.checkPass(5)
		require.True(t, pass)
		require.True(t, wanted)

		pass, _ = g.checkPass(5)
		require.False(t, pass)

		g.addWanted(5)
		pass, wanted = g.checkPass(5)
		require.False(t, pass)
		require.False(t, wanted)
	})
}
