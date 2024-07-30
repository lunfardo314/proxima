package util

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatchPanicOrError(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		err := CatchPanicOrError(func() error {
			return nil
		})
		require.NoError(t, err)
	})
	t.Run("2", func(t *testing.T) {
		err := CatchPanicOrError(func() error {
			return errors.New("--- error 314")
		})
		RequireErrorWith(t, err, "--- error", "314")
	})
	t.Run("return error", func(t *testing.T) {
		err := CatchPanicOrError(func() error {
			return errors.New("--- error 314")
		}, true)
		RequireErrorWith(t, err, "--- error", "314")
		//t.Logf("ERROR: %v", err)
	})
	t.Run("nil pointer", func(t *testing.T) {
		err := CatchPanicOrError(func() error {
			var pnil *int
			fmt.Println(*pnil)
			return errors.New("no panic")
		}, true)
		RequireErrorWith(t, err, "invalid memory address or nil pointer dereference")
		//t.Logf("ERROR: %v", err)
	})

}
