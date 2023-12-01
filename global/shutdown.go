package global

import "go.uber.org/atomic"

var isShuttingDown atomic.Bool

func IsShuttingDown() bool {
	return isShuttingDown.Load()
}

func SetShutDown() {
	isShuttingDown.Store(true)
}
