package util

import (
	"fmt"
	"runtime/debug"
)

func CatchPanicOrError(f func() error, includeStack ...bool) error {
	var err error
	var stack string
	addStack := len(includeStack) > 0 && includeStack[0]
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			if addStack {
				stack = string(debug.Stack())
			}
			var ok bool
			if err, ok = r.(error); !ok {
				err = fmt.Errorf("%v (err type=%T)", r, r)
			}
		}()
		err = f()
	}()
	if err != nil && addStack {
		err = fmt.Errorf("%w\n%s", err, stack) // %w is essential, otherwise does not catch the error
	}
	return err
}

func RunWrappedRoutine(name string, fun func(), onPanic func(err error) bool) {
	go func() {
		err := CatchPanicOrError(func() error {
			fun()
			return nil
		}, true)
		if err == nil {
			return
		}
		if onPanic == nil || onPanic(err) {
			// if not suppressed, rise panic
			panic(fmt.Errorf("panic in '%s': %v (err type = %T)", name, err, err))
		}
	}()
}
