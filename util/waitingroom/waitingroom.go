package waitingroom

import (
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/util"
	"go.uber.org/atomic"
)

type (
	WaitingRoom struct {
		mutex   sync.Mutex
		waiting []*wrRec
		period  time.Duration
		stopped atomic.Bool
	}

	wrRec struct {
		deadline time.Time
		fun      func()
	}
)

const defaultPoolingPeriod = 100 * time.Millisecond

func Create(poolEvery ...time.Duration) *WaitingRoom {
	ret := &WaitingRoom{
		waiting: make([]*wrRec, 0),
		period:  defaultPoolingPeriod,
	}
	if len(poolEvery) > 0 {
		ret.period = poolEvery[0]
	}

	go ret.loop()
	return ret
}

func (d *WaitingRoom) loop() {
	for {
		time.Sleep(d.period)
		if d.stopped.Load() {
			return
		}
		toRun := d.selectRunnable()
		sort.Slice(toRun, func(i, j int) bool {
			return toRun[i].deadline.Before(toRun[j].deadline)
		})
		for _, e := range toRun {
			e.fun()
		}
	}
}

func (d *WaitingRoom) selectRunnable() []*wrRec {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	nowis := time.Now()
	remaining := make([]*wrRec, 0)
	toRun := make([]*wrRec, 0)

	for _, rec := range d.waiting {
		if rec.deadline.After(nowis) {
			remaining = append(remaining, rec)
		} else {
			toRun = append(toRun, rec)
		}
	}
	d.waiting = remaining
	return toRun
}

func (d *WaitingRoom) Stop() {
	d.stopped.Store(true)
}

func (d *WaitingRoom) RunAfter(t time.Time, fun func()) {
	util.Assertf(!d.stopped.Load(), "WaitingRoom already stopped")
	if t.Before(time.Now()) {
		fun()
		return
	}
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.waiting = append(d.waiting, &wrRec{
		deadline: t,
		fun:      fun,
	})
}

func (d *WaitingRoom) RunAfterUnixNano(t int64, fun func()) {
	d.RunAfter(time.Unix(0, t), fun)
}

func (d *WaitingRoom) RunDelayed(t time.Duration, fun func()) {
	d.RunAfter(time.Now().Add(t), fun)
}
