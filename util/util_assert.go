package util

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func EvalLazyArgs(args ...any) []any {
	ret := make([]any, len(args))
	for i, arg := range args {
		switch funArg := arg.(type) {
		case func() any:
			ret[i] = funArg()
		case func() string:
			ret[i] = funArg()
		default:
			ret[i] = arg
		}
	}
	return ret
}

// Assertf with optionally deferred evaluation of arguments
func Assertf(cond bool, format string, args ...any) {
	if !cond {
		panic(fmt.Errorf("assertion failed:: "+format, EvalLazyArgs(args...)...))
	}
}

func ErrorConditionf(cond bool, format string, args ...any) error {
	if !cond {
		return fmt.Errorf("assertion failed:: "+format, EvalLazyArgs(args...)...)
	}
	return nil
}

func Panicf(format string, args ...any) {
	Assertf(false, format, args...)
}

func AssertNoError(err error, prefix ...string) {
	pref := "error: "
	if len(prefix) > 0 {
		pref = strings.Join(prefix, " ") + ": "
	}
	Assertf(err == nil, pref+"%w", err)
}

func IsNil(p interface{}) bool {
	return p == nil || (reflect.ValueOf(p).Kind() == reflect.Ptr && reflect.ValueOf(p).IsNil())
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
