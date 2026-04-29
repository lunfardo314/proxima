package global

import "time"

func (l *Global) RepeatInBackground(name string, period time.Duration, fun func() bool, skipFirst ...bool) {
	l.MarkWorkProcessStarted(name)
	l.LogTopicf("lifecycle", 0, "[%s] STARTED", name)

	go func() {
		defer func() {
			l.MarkWorkProcessStopped(name)
			l.LogTopicf("lifecycle", 0, "[%s] STOPPED", name)
		}()

		if len(skipFirst) == 0 || !skipFirst[0] {
			if !fun() {
				return
			}
		}
		l.RepeatSync(period, fun)
	}()
}

func (l *Global) RepeatSync(period time.Duration, fun func() bool) bool {
	timer := time.NewTimer(period)
	defer timer.Stop()

	for {
		select {
		case <-l.Ctx().Done():
			return false
		case <-timer.C:
			if !fun() {
				return true
			}
			timer.Reset(period)
		}
	}
}
