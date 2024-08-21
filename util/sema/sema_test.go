package sema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSema(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		s := New()
		s.Lock()
		s.Unlock()
		s.RLock()
		s.RUnlock()
	})
	t.Run("2", func(t *testing.T) {
		s := New(time.Second)
		s.Lock()
		s.Unlock()
		s.RLock()
		s.RUnlock()
	})
	t.Run("3", func(t *testing.T) {
		s := New(time.Second)
		s.Lock()
		require.Panics(t, func() {
			s.Lock()
		})
	})
	t.Run("4", func(t *testing.T) {
		s := New(time.Second)
		s.RLock()
		require.Panics(t, func() {
			s.RLock()
		})
	})
	t.Run("5", func(t *testing.T) {
		s := New(time.Second)
		require.Panics(t, func() {
			s.Unlock()
		})
	})
}
