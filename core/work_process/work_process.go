package work_process

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	environment interface {
		global.NodeGlobal
	}

	WorkProcess[T any] struct {
		environment
		*queue.Queue[T]
		Name     string
		consumer func(inp T)
	}
)

func New[T any](env environment, name string, consumer func(inp T)) *WorkProcess[T] {
	return &WorkProcess[T]{
		environment: env,
		Name:        name,
		consumer:    consumer,
	}
}

func (wp *WorkProcess[T]) Start() {
	wp.Queue = queue.New(wp.consumer)
	wp.MarkWorkProcessStarted(wp.Name)
	wp.Log().Infof("[%s] STARTED", wp.Name)

	go func() {
		// work process stops by observing closing global context
		<-wp.Ctx().Done()

		wp.Queue.Close(false)
		wp.MarkWorkProcessStopped(wp.Name)
		wp.Log().Infof("[%s] STOPPED", wp.Name)
	}()
}
