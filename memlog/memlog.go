package memlog

import (
	"context"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/general"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

const (
	memLogFrequency = 10 * time.Second
	memoryLogName   = "[mem]"
)

func StartMemoryLogging(ctx context.Context) {
	var log *zap.SugaredLogger

	if logOut := viper.GetString("logger.output"); logOut == "" {
		log = general.NewLogger(memoryLogName, zap.InfoLevel, []string{"stdout"}, "")
	} else {
		log = general.NewLogger(memoryLogName, zap.InfoLevel, []string{"stdout", logOut}, "")
	}
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
			log.Infof("allocated: %.1f MB, system: %.1f MB, Num GC: %d, Goroutines: %d, ",
				float32(mstats.Alloc*10/(1024*1024))/10,
				float32(mstats.Sys*10/(1024*1024))/10,
				mstats.NumGC,
				runtime.NumGoroutine(),
			)
		}
	}()
	go func() {
		<-ctx.Done()
		close(stopChan)
	}()
}
