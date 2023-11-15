package util

import (
	"errors"
	"fmt"
)

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

func RunWrappedRoutine(name string, fun func(), ignore ...error) {
	go func() {
		err := CatchPanicOrError(func() error {
			fun()
			return nil
		})
		if err == nil {
			return
		}
		for _, e := range ignore {
			if errors.Is(err, e) {
				return
			}
		}
		panic(fmt.Errorf("uncaught panic in '%s': %v", name, err))
	}()
}
