package countdown

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/util"
	"go.uber.org/atomic"
)

type Countdown struct {
	counter     atomic.Int32
	timeout     time.Duration
	deadlineSet bool
	name        string
	from        int32
}

func New(from int, timeout ...time.Duration) *Countdown {
	return NewNamed("", from, timeout...)
}

func NewNamed(name string, from int, timeout ...time.Duration) *Countdown {
	util.Assertf(from > 0, "countdown starting point must be > 0")
	ret := &Countdown{
		name:        name,
		from:        int32(from),
		deadlineSet: len(timeout) > 0,
	}
	ret.counter.Store(ret.from)
	if ret.deadlineSet {
		ret.timeout = timeout[0]
	}
	return ret
}

func (c *Countdown) Tick() {
	c.counter.Dec()
}

func (c *Countdown) Wait() error {
	var deadline time.Time
	if c.deadlineSet {
		deadline = time.Now().Add(c.timeout)
	}
	n := c.name
	if n == "" {
		n = " "
	} else {
		n = " '" + n + "' "
	}

	for {
		v := c.counter.Load()
		switch {
		case v > 0:
			if c.deadlineSet && time.Now().After(deadline) {
				return fmt.Errorf("countdown%sfrom %d: timeout %v expired at counter value %d",
					n, c.from, c.timeout, v)
			}
		case v == 0:
			return nil
		case v < 0:
			return fmt.Errorf("countdown%sfrom %d: underflow at counter value %d", n, c.from, v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Wait wait for multiple countdowns to finish
func Wait(cd ...*Countdown) error {
	var wg sync.WaitGroup

	wg.Add(len(cd))
	errs := make([]error, len(cd))

	for i, c := range cd {
		c1 := c
		i1 := i
		go func() {
			errs[i1] = c1.Wait()
			wg.Done()
		}()
	}
	wg.Wait()

	buf := make([]string, 0)
	hasErrors := false
	for i, err := range errs {
		if err != nil {
			hasErrors = true
			buf = append(buf, fmt.Sprintf("#%d: '%s'", i, err.Error()))
		}
	}
	if hasErrors {
		return errors.New(strings.Join(buf, "\n"))
	}
	return nil
}
