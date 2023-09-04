package workerpool

import (
	"github.com/lunfardo314/proxima/util"
)

type WorkerPool chan struct{}

func NewWorkerPool(maxWorkers int) WorkerPool {
	util.Assertf(maxWorkers > 0, "maximum workers parameter must be positive")
	return make(chan struct{}, maxWorkers)
}

func (wp WorkerPool) Work(fun func()) {
	wp <- struct{}{}
	go func() {
		fun()
		<-wp
	}()
}

func (wp WorkerPool) Len() int {
	return len(wp)
}
