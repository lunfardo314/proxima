package util

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Assertf with optionally deferred evaluation of arguments
func Assertf(cond bool, format string, args ...any) {
	if !cond {
		args1 := make([]any, len(args))
		for i, arg := range args {
			if arg1, isClosure := arg.(func() any); isClosure {
				args1[i] = arg1()
			} else {
				args1[i] = arg
			}
		}
		panic(fmt.Sprintf("assertion failed:: "+format, args1...))
	}
}

func Panicf(format string, args ...any) {
	Assertf(false, format, args...)
}

func AssertNoError(err error, prefix ...string) {
	pref := "error: "
	if len(prefix) > 0 {
		pref = strings.Join(prefix, " ") + ": "
	}
	Assertf(err == nil, pref+"%v", err)
}

func IsNil(p interface{}) bool {
	return p == nil || (reflect.ValueOf(p).Kind() == reflect.Ptr && reflect.ValueOf(p).IsNil())
}

func CatchPanicOrError(f func() error) error {
	var err error
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			var ok bool
			if err, ok = r.(error); !ok {
				err = fmt.Errorf("%v", r)
			}
		}()
		err = f()
	}()
	return err
}

func RequireErrorWith(t *testing.T, err error, fragments ...string) {
	require.Error(t, err)
	for _, f := range fragments {
		require.Contains(t, err.Error(), f)
	}
}

func RequirePanicOrErrorWith(t *testing.T, f func() error, fragments ...string) {
	RequireErrorWith(t, CatchPanicOrError(f), fragments...)
}
