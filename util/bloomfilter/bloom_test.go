package bloomfilter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBase(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		f := New[int](context.Background(), 10*time.Second)
		require.EqualValues(t, 0, f.Len())
	})

	t.Run("1", func(t *testing.T) {
		f := New[int](context.Background(), 10*time.Second)
		require.EqualValues(t, 0, f.Len())

		hit := f.CheckAndUpdate(1)
		require.False(t, hit)
		require.EqualValues(t, 1, f.Len())

		hit = f.CheckAndDelete(1)
		require.True(t, hit)
		require.EqualValues(t, 0, f.Len())

		hit = f.CheckAndUpdate(5)
		require.False(t, hit)
		hit = f.CheckAndUpdate(5)
		require.True(t, hit)
		require.EqualValues(t, 1, f.Len())

		hit = f.CheckAndDelete(1)
		require.False(t, hit)
		require.EqualValues(t, 1, f.Len())

		hit = f.CheckAndUpdate(5)
		require.True(t, hit)
		require.EqualValues(t, 1, f.Len())

		hit = f.CheckAndDelete(5)
		require.True(t, hit)
		require.EqualValues(t, 0, f.Len())
	})

}
