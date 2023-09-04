package eventtype

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBase(t *testing.T) {
	cInt := RegisterNew[int]("cInt")
	cBool := RegisterNew[bool]("cBool")
	cUint64 := RegisterNew[uint64]("cUint64")
	cUint64_1 := RegisterNew[uint64]("cUint64-1")

	h1, err := MakeHandler(cInt, func(i int) {
		fmt.Printf("%d\n", i*2)
	})
	require.NoError(t, err)
	h1(5)

	_, err = MakeHandler(cBool, func(i bool) {
	})
	require.NoError(t, err)

	err = CheckArgType(cInt, 1337)
	require.NoError(t, err)
	err = CheckArgType(cInt, true)
	require.Error(t, err)

	_, err = MakeHandler(cUint64, func(i uint64) {
	})
	require.NoError(t, err)
	h1, err = MakeHandler(cUint64_1, func(i uint64) {
		fmt.Printf("%d\n", i*3)
	})
	require.NoError(t, err)
	h1(uint64(5))

	_, err = MakeHandler(cUint64, func(i int) {
	})
	require.Error(t, err)

}
