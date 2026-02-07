package util

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/lunfardo314/proxima/util/lazyargs"
)

// Assertf with optionally deferred evaluation of arguments
func Assertf(cond bool, format string, args ...any) {
	if !cond {
		panic(fmt.Errorf("assertion failed:: "+format, lazyargs.Eval(args...)...))
	}
}

func ErrorCondf(cond bool, format string, args ...any) error {
	if !cond {
		return fmt.Errorf("assertion failed:: "+format, lazyargs.Eval(args...)...)
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

func AssertMustError(err error, target ...error) {
	Assertf(err != nil, "error expected")
	if len(target) > 0 {
		Assertf(errors.Is(err, target[0]), "error '%s' was expected", target[0])
	} else {
		Assertf(err != nil, "an error was expected")
	}
}

func IsNil(p interface{}) bool {
	return p == nil || (reflect.ValueOf(p).Kind() == reflect.Ptr && reflect.ValueOf(p).IsNil())
}

func MustErrorWith(err error, fragments ...string) error {
	if err == nil {
		return fmt.Errorf("-------------- error was expected -------------------")
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			return fmt.Errorf("\n-------------- error does not contain required fragment -------------------\nERROR: %w\nREQUIRED FRAGMENT: '%s'", err, f)
		}
	}
	return nil
}

