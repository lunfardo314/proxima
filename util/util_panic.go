package util

import (
	"errors"
	"fmt"
	"runtime/debug"
)

func CatchPanicOrError(f func() error, includeStack ...bool) error {
	var err error
	var stack string
	takeStack := len(includeStack) > 0 && includeStack[0]
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			if takeStack {
				stack = string(debug.Stack())
			}
			var ok bool
			if err, ok = r.(error); !ok {
				err = fmt.Errorf("%v (err type=%T)", r, r)
			}
		}()
		err = f()
	}()
	if err != nil && takeStack {
		err = fmt.Errorf("%w\n%s", err, stack) // %w is essential, otherwise does not catch the error
	}
	return err
}

func RunWrappedRoutine(name string, fun func(), onUncaughtPanic func(err error), ignore ...error) {
	go func() {
		err := CatchPanicOrError(func() error {
			fun()
			return nil
		}, true)
		if err == nil {
			return
		}
		for _, e := range ignore {
			if errors.Is(err, e) {
				return
			}
		}
		err = fmt.Errorf("uncaught panic in '%s': %v (err type = %T)", name, err, err)
		if onUncaughtPanic != nil {
			onUncaughtPanic(err)
		} else {
			panic(err)
		}
	}()
}
