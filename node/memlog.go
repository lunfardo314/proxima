package node

import (
	"runtime"
	"time"
)

const memLogFrequency = 5 * time.Second

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

			sync := "NO SYNC"
			if p.uTangle.SyncData().IsSynced() {
				sync = "SYNC"
			}
			runtime.ReadMemStats(&mstats)
			p.log.Infof("%s, allocated %.1f MB, system %.1f MB, Num GC: %d, Goroutines: %d, ",
				sync,
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
