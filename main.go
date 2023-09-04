package main

import (
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap/zapcore"
)

func main() {
	log := util.NewNamedLogger("-top-", zapcore.InfoLevel)
	log.Info(general.BannerString())
}
