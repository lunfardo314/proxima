package node

import (
	"runtime"
	"time"
)

const memLogFrequency = 10 * time.Second

func (p *ProximaNode) startMemoryLogging() {
	stopChan := make(chan struct{})
	var mstats runtime.MemStats

	go func() {
		for {
			select {
			case <-stopChan:
				return
			case <-time.After(memLogFrequency):
			}

			runtime.ReadMemStats(&mstats)
			p.log.Infof("memory: allocated: %.1f MB, system: %.1f MB, Num GC: %d, Goroutines: %d, ",
				float32(mstats.Alloc*10/(1024*1024))/10,
				float32(mstats.Sys*10/(1024*1024))/10,
				mstats.NumGC,
				runtime.NumGoroutine(),
			)
		}
	}()
	go func() {
		<-p.ctx.Done()
		close(stopChan)
	}()
}
