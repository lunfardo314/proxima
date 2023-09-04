package wait

import "time"

type Delay struct {
	w *WaitingRoom[struct{}]
}

func NewDelay() *Delay {
	return &Delay{
		w: New[struct{}](),
	}
}

func (d *Delay) RunAfterDeadline(deadline time.Time, fun func()) {
	d.w.RunAtOrBefore(deadline, func(_ struct{}) {
		fun()
	})
}

func (d *Delay) RunAfterUnixNano(t int64, fun func()) {
	d.RunAfterDeadline(time.Unix(0, t), fun)
}

func (d *Delay) Stop() {
	d.w.Stop()
}
